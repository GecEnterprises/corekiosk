package ckdiscovery

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/grandcat/zeroconf"
)

// ScanOptions configures a client-side discovery scan.
type ScanOptions struct {
	Timeout time.Duration
	Methods []string
	UDPPort int
}

// ScanResult is one merged discovery candidate. Hosts may contain more than one
// address when the same server is seen through multiple transports/interfaces.
type ScanResult struct {
	ServerID        string    `json:"server_id"`
	Name            string    `json:"name,omitempty"`
	AppName         string    `json:"app_name,omitempty"`
	Hosts           []string  `json:"hosts"`
	Port            int       `json:"port"`
	ProtocolVersion int       `json:"protocol_version,omitempty"`
	Version         string    `json:"version,omitempty"`
	Methods         []string  `json:"methods,omitempty"`
	LastSeen        time.Time `json:"last_seen"`
}

// Scan probes the LAN using the requested transports and returns merged results.
func Scan(ctx context.Context, opts ScanOptions) ([]ScanResult, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultScanTimeout * time.Millisecond
	}
	if opts.UDPPort <= 0 {
		opts.UDPPort = DefaultUDPPort
	}
	methods := normalizeScanMethods(opts.Methods)
	scanCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var (
		mu      sync.Mutex
		results []ScanResult
		errs    []error
	)
	var wg sync.WaitGroup
	add := func(r ScanResult) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}
	for _, method := range methods {
		wg.Add(1)
		go func(method string) {
			defer wg.Done()
			var err error
			switch method {
			case MethodUDP:
				err = browseUDP(scanCtx, opts.UDPPort, add)
			case MethodMDNS:
				err = browseMDNS(scanCtx, add)
			case MethodSSDP:
				err = browseSSDP(scanCtx, add)
			}
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", method, err))
				mu.Unlock()
			}
		}(method)
	}
	wg.Wait()

	merged := mergeScanResults(results)
	if len(merged) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return merged, nil
}

func normalizeScanMethods(methods []string) []string {
	if len(methods) == 0 {
		return []string{MethodUDP, MethodMDNS, MethodSSDP}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(methods))
	for _, raw := range methods {
		m := strings.ToLower(strings.TrimSpace(raw))
		switch m {
		case MethodUDP, MethodMDNS, MethodSSDP:
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return []string{MethodUDP, MethodMDNS, MethodSSDP}
	}
	return out
}

func browseUDP(ctx context.Context, port int, add func(ScanResult)) error {
	lc := net.ListenConfig{Control: enableBroadcastControl}
	pc, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return ErrInvalidPacket
	}
	defer conn.Close()

	nonce := randomNonce()
	probe := ckframework.DiscoveryProbe{
		CK:       ckframework.DiscoveryProtocolVersion,
		Type:     "probe",
		Nonce:    nonce,
		Protocol: ckframework.DiscoveryProtocolVersion,
	}
	payload, _ := json.Marshal(probe)
	targets := broadcastTargets(port)
	for _, target := range targets {
		_, _ = conn.WriteToUDP(payload, target)
	}

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return nil
		}
		offer, err := DecodeOffer(buf[:n])
		if err != nil || offer.Nonce != nonce || offer.ServerID == "" {
			continue
		}
		host := ""
		if src != nil {
			host = src.IP.String()
		}
		add(ScanResult{
			ServerID:        offer.ServerID,
			Name:            offer.Name,
			AppName:         offer.AppName,
			Hosts:           nonEmptyHosts(host),
			Port:            offer.Port,
			ProtocolVersion: offer.Protocol,
			Version:         offer.Version,
			Methods:         []string{MethodUDP},
			LastSeen:        time.Now(),
		})
	}
}

func browseMDNS(ctx context.Context, add func(ScanResult)) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}
	entries := make(chan *zeroconf.ServiceEntry, 16)
	go func() {
		for entry := range entries {
			fields := parseTXT(entry.Text)
			serverID := fields["id"]
			if serverID == "" {
				continue
			}
			hosts := make([]string, 0, len(entry.AddrIPv4)+len(entry.AddrIPv6))
			for _, ip := range entry.AddrIPv4 {
				if ip != nil {
					hosts = append(hosts, ip.String())
				}
			}
			for _, ip := range entry.AddrIPv6 {
				if ip != nil {
					hosts = append(hosts, ip.String())
				}
			}
			proto := atoi(fields["proto"])
			add(ScanResult{
				ServerID:        serverID,
				Name:            cleanInstance(entry.Instance),
				AppName:         fields["app"],
				Hosts:           hosts,
				Port:            entry.Port,
				ProtocolVersion: proto,
				Version:         fields["v"],
				Methods:         []string{MethodMDNS},
				LastSeen:        time.Now(),
			})
		}
	}()
	service := strings.TrimSuffix(ckframework.DiscoveryServiceType, ".")
	if err := resolver.Browse(ctx, service, "local.", entries); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func browseSSDP(ctx context.Context, add func(ScanResult)) error {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + ckframework.DiscoverySSDPType + "\r\n\r\n"
	if _, err := conn.WriteToUDP([]byte(msg), ssdpGroup); err != nil {
		return err
	}
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return nil
		}
		host, port, serverID, name, ok := parseSSDPResponse(buf[:n])
		if !ok {
			continue
		}
		if host == "" && src != nil {
			host = src.IP.String()
		}
		add(ScanResult{
			ServerID: serverID,
			Name:     name,
			Hosts:    nonEmptyHosts(host),
			Port:     port,
			Methods:  []string{MethodSSDP},
			LastSeen: time.Now(),
		})
	}
}

func broadcastTargets(port int) []*net.UDPAddr {
	targets := []*net.UDPAddr{{IP: net.IPv4bcast, Port: port}}
	ifaces, err := net.Interfaces()
	if err != nil {
		return targets
	}
	seen := map[string]struct{}{}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLinkLocalUnicast() {
				continue
			}
			mask := net.IP(ipnet.Mask).To4()
			if mask == nil {
				continue
			}
			bcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bcast[i] = ip4[i] | ^mask[i]
			}
			key := bcast.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, &net.UDPAddr{IP: bcast, Port: port})
		}
	}
	return targets
}

func mergeScanResults(in []ScanResult) []ScanResult {
	byID := map[string]*ScanResult{}
	order := make([]string, 0, len(in))
	for _, r := range in {
		if r.ServerID == "" {
			continue
		}
		cur, ok := byID[r.ServerID]
		if !ok {
			cp := r
			cp.Hosts = uniqueStrings(r.Hosts)
			cp.Methods = uniqueStrings(r.Methods)
			byID[r.ServerID] = &cp
			order = append(order, r.ServerID)
			continue
		}
		cur.Hosts = uniqueStrings(append(cur.Hosts, r.Hosts...))
		cur.Methods = uniqueStrings(append(cur.Methods, r.Methods...))
		if cur.Name == "" {
			cur.Name = r.Name
		}
		if cur.AppName == "" {
			cur.AppName = r.AppName
		}
		if cur.Port == 0 {
			cur.Port = r.Port
		}
		if cur.Version == "" {
			cur.Version = r.Version
		}
		if cur.ProtocolVersion == 0 {
			cur.ProtocolVersion = r.ProtocolVersion
		}
		if r.LastSeen.After(cur.LastSeen) {
			cur.LastSeen = r.LastSeen
		}
	}
	out := make([]ScanResult, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func parseTXT(text []string) map[string]string {
	out := map[string]string{}
	for _, raw := range text {
		i := strings.IndexByte(raw, '=')
		if i <= 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(raw[:i]))] = strings.TrimSpace(raw[i+1:])
	}
	return out
}

func cleanInstance(instance string) string {
	instance = strings.TrimSpace(instance)
	if i := strings.LastIndex(instance, " ["); i > 0 && strings.HasSuffix(instance, "]") {
		instance = instance[:i]
	}
	return instance
}

func nonEmptyHosts(hosts ...string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func randomNonce() string {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func atoi(v string) int {
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
