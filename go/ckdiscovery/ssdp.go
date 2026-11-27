package ckdiscovery

import (
	"context"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"golang.org/x/net/ipv4"
)

var ssdpGroup = &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 1900}

const ssdpMaxAge = 1800

type ssdpAdvertiser struct {
	cfg     Config
	getInfo func() Info

	mu     sync.Mutex
	conn   *net.UDPConn
	packet *ipv4.PacketConn
	cancel context.CancelFunc
	wg     sync.WaitGroup
	close  sync.Once
}

func newSSDPAdvertiser(cfg Config, getInfo func() Info) *ssdpAdvertiser {
	return &ssdpAdvertiser{cfg: cfg, getInfo: getInfo}
}

func (s *ssdpAdvertiser) Start(ctx context.Context) error {
	lc := net.ListenConfig{Control: reuseAddressControl}
	pc, err := lc.ListenPacket(ctx, "udp4", ":1900")
	if err != nil {
		return err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return ErrInvalidPacket
	}
	packet := ipv4.NewPacketConn(conn)
	_ = packet.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)

	ifaces := multicastInterfaces()
	joined := 0
	for _, ifi := range ifaces {
		if err := packet.JoinGroup(&ifi, ssdpGroup); err == nil {
			joined++
		}
	}
	if joined == 0 {
		_ = conn.Close()
		return ErrInvalidPacket
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.conn = conn
	s.packet = packet
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go s.readLoop(runCtx, packet)
	s.wg.Add(1)
	go s.announceLoop(runCtx)
	s.sendAlive()
	return nil
}

func (s *ssdpAdvertiser) Update(Info) {
	s.sendAlive()
}

func (s *ssdpAdvertiser) Close() error {
	s.close.Do(func() {
		s.sendByebye()
		s.mu.Lock()
		cancel := s.cancel
		conn := s.conn
		s.cancel = nil
		s.conn = nil
		s.packet = nil
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if conn != nil {
			_ = conn.Close()
		}
		s.wg.Wait()
	})
	return nil
}

func (s *ssdpAdvertiser) readLoop(ctx context.Context, packet *ipv4.PacketConn) {
	defer s.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = packet.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
		n, cm, src, err := packet.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		udpSrc, ok := src.(*net.UDPAddr)
		if !ok || udpSrc == nil || !IsPrivateSource(udpSrc.IP) {
			continue
		}
		st, ok := parseMSearch(buf[:n])
		if !ok {
			continue
		}
		if st != ckframework.DiscoverySSDPType && st != "ssdp:all" {
			continue
		}
		info := s.getInfo()
		host := info.Addresses
		if cm != nil {
			if ifip := interfaceIPv4(cm.IfIndex); ifip != "" {
				host = []string{ifip}
			}
		}
		if len(host) == 0 {
			continue
		}
		resp := buildMSearchResponse(info, host[0])
		delay := time.Duration(rand.Intn(100)) * time.Millisecond
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			_, _ = packet.WriteTo([]byte(resp), nil, src)
		}()
	}
}

func (s *ssdpAdvertiser) announceLoop(ctx context.Context) {
	defer s.wg.Done()
	every := time.Duration(s.cfg.advertiseEvery()) * time.Second
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sendAlive()
		}
	}
}

func (s *ssdpAdvertiser) sendAlive() {
	s.sendNotify("ssdp:alive")
}

func (s *ssdpAdvertiser) sendByebye() {
	s.sendNotify("ssdp:byebye")
}

func (s *ssdpAdvertiser) sendNotify(nts string) {
	s.mu.Lock()
	packet := s.packet
	s.mu.Unlock()
	if packet == nil {
		return
	}
	info := s.getInfo()
	for _, ifi := range multicastInterfaces() {
		ip := interfaceIPv4(ifi.Index)
		if ip == "" {
			if len(info.Addresses) == 0 {
				continue
			}
			ip = info.Addresses[0]
		}
		msg := buildNotify(info, ip, nts)
		cm := &ipv4.ControlMessage{IfIndex: ifi.Index}
		_, _ = packet.WriteTo([]byte(msg), cm, ssdpGroup)
	}
}

func buildNotify(info Info, ip string, nts string) string {
	var b strings.Builder
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	b.WriteString("HOST: 239.255.255.250:1900\r\n")
	b.WriteString("NT: " + ckframework.DiscoverySSDPType + "\r\n")
	b.WriteString("NTS: " + nts + "\r\n")
	b.WriteString("USN: " + ssdpUSN(info.ServerID) + "\r\n")
	b.WriteString("LOCATION: " + discoveryLocation(info, ip) + "\r\n")
	b.WriteString("CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge) + "\r\n")
	b.WriteString("SERVER: corekiosk/" + info.Version + " UPnP/1.1\r\n")
	b.WriteString("X-CK-ID: " + info.ServerID + "\r\n")
	b.WriteString("X-CK-NAME: " + SanitizeName(info.Name, info.AppName) + "\r\n")
	b.WriteString("\r\n")
	return b.String()
}

func buildMSearchResponse(info Info, ip string) string {
	var b strings.Builder
	b.WriteString("HTTP/1.1 200 OK\r\n")
	b.WriteString("CACHE-CONTROL: max-age=" + strconv.Itoa(ssdpMaxAge) + "\r\n")
	b.WriteString("DATE: " + time.Now().UTC().Format(time.RFC1123) + "\r\n")
	b.WriteString("EXT:\r\n")
	b.WriteString("LOCATION: " + discoveryLocation(info, ip) + "\r\n")
	b.WriteString("SERVER: corekiosk/" + info.Version + " UPnP/1.1\r\n")
	b.WriteString("ST: " + ckframework.DiscoverySSDPType + "\r\n")
	b.WriteString("USN: " + ssdpUSN(info.ServerID) + "\r\n")
	b.WriteString("X-CK-ID: " + info.ServerID + "\r\n")
	b.WriteString("X-CK-NAME: " + SanitizeName(info.Name, info.AppName) + "\r\n")
	b.WriteString("\r\n")
	return b.String()
}

func discoveryLocation(info Info, ip string) string {
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(info.HTTPPort)) + "/v1/health"
}

func ssdpUSN(serverID string) string {
	return "urn:gec:corekiosk:" + serverID
}

func parseMSearch(data []byte) (string, bool) {
	text := string(data)
	lines := strings.Split(text, "\r\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.ToUpper(lines[0]), "M-SEARCH ") {
		return "", false
	}
	headers := parseHeaders(lines[1:])
	if !strings.Contains(strings.ToLower(headers["man"]), "ssdp:discover") {
		return "", false
	}
	st := strings.TrimSpace(headers["st"])
	return st, st != ""
}

func parseHeaders(lines []string) map[string]string {
	out := map[string]string{}
	for _, line := range lines {
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		out[key] = strings.TrimSpace(line[i+1:])
	}
	return out
}

func parseSSDPResponse(data []byte) (host string, port int, serverID string, name string, ok bool) {
	lines := strings.Split(string(data), "\r\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.ToUpper(lines[0]), "HTTP/1.1 200") {
		return "", 0, "", "", false
	}
	headers := parseHeaders(lines[1:])
	loc := headers["location"]
	serverID = headers["x-ck-id"]
	name = headers["x-ck-name"]
	if serverID == "" {
		usn := headers["usn"]
		if strings.HasPrefix(usn, "urn:gec:corekiosk:") {
			serverID = strings.TrimPrefix(usn, "urn:gec:corekiosk:")
		}
	}
	u, err := url.Parse(loc)
	if err != nil || u.Hostname() == "" {
		return "", 0, "", "", false
	}
	p := u.Port()
	if p == "" {
		p = "80"
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 {
		return "", 0, "", "", false
	}
	return u.Hostname(), n, serverID, name, serverID != ""
}

func multicastInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]net.Interface, 0, len(ifaces))
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, ifi)
	}
	if len(out) == 0 {
		for _, ifi := range ifaces {
			if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagLoopback == 0 {
				out = append(out, ifi)
			}
		}
	}
	return out
}

func interfaceIPv4(index int) string {
	ifi, err := net.InterfaceByIndex(index)
	if err != nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		return ip4.String()
	}
	return ""
}
