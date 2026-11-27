package ckdiscovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

const (
	// MethodUDP is the UDP broadcast probe/offer transport.
	MethodUDP = "udp"
	// MethodMDNS is DNS-SD / mDNS.
	MethodMDNS = "mdns"
	// MethodSSDP is SSDP / UPnP-style discovery.
	MethodSSDP = "ssdp"
	// DefaultUDPPort is the UDP probe/offer port.
	DefaultUDPPort = ckframework.DiscoveryDefaultUDPPort
	// DefaultAdvertiseEvery is how often mDNS/SSDP announcements are refreshed.
	DefaultAdvertiseEvery = 15 * 60 // seconds
	// DefaultScanTimeout is the recommended companion scan window.
	DefaultScanTimeout = 3500 // milliseconds

	maxPacketSize = 1024
)

var (
	// ErrInvalidPacket is returned for malformed or foreign discovery packets.
	ErrInvalidPacket = errors.New("invalid discovery packet")
)

// Config controls the server-side advertiser.
type Config struct {
	Enabled        bool
	Name           string
	Methods        []string
	UDPPort        int
	AdvertiseEvery int // seconds; 0 uses DefaultAdvertiseEvery
}

// ManifestConfig is the JSON shape stored in kiosk.json and kiosk.manifest.json.
// Enabled is a pointer so "absent" can default to true for the kiosk launcher.
type ManifestConfig struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	Name           string   `json:"name,omitempty"`
	Methods        []string `json:"methods,omitempty"`
	UDPPort        int      `json:"udp_port,omitempty"`
	AdvertiseEvery int      `json:"advertise_interval_seconds,omitempty"`
}

// ToConfig converts the manifest shape to a runtime Config.
func (m ManifestConfig) ToConfig(defaultEnabled bool) Config {
	enabled := defaultEnabled
	if m.Enabled != nil {
		enabled = *m.Enabled
	}
	return Config{
		Enabled:        enabled,
		Name:           m.Name,
		Methods:        append([]string(nil), m.Methods...),
		UDPPort:        m.UDPPort,
		AdvertiseEvery: m.AdvertiseEvery,
	}
}

// Info is the live identity/state advertised by the server.
type Info struct {
	ServerID        string
	ServerPub       string
	AppName         string
	Name            string
	HTTPPort        int
	ProtocolVersion int
	Version         string
	Addresses       []string
	Methods         []string
}

// Status is a snapshot suitable for the launcher UI and /v1/health.
type Status struct {
	Enabled   bool              `json:"enabled"`
	Name      string            `json:"name,omitempty"`
	Methods   []string          `json:"methods,omitempty"`
	Active    []string          `json:"active,omitempty"`
	UDPPort   int               `json:"udp_port,omitempty"`
	Port      int               `json:"port,omitempty"`
	Addresses []string          `json:"addresses,omitempty"`
	Errors    map[string]string `json:"errors,omitempty"`
}

// Methods returns the normalized configured methods.
func (c Config) normalizedMethods() []string {
	if len(c.Methods) == 0 {
		return []string{MethodUDP, MethodMDNS}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(c.Methods))
	for _, raw := range c.Methods {
		m := strings.ToLower(strings.TrimSpace(raw))
		if m == "" {
			continue
		}
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
		return []string{MethodUDP, MethodMDNS}
	}
	return out
}

func (c Config) udpPort() int {
	if c.UDPPort > 0 && c.UDPPort < 65536 {
		return c.UDPPort
	}
	return DefaultUDPPort
}

func (c Config) advertiseEvery() int {
	if c.AdvertiseEvery > 0 {
		return c.AdvertiseEvery
	}
	return DefaultAdvertiseEvery
}

// BuildOffer constructs the reply for a probe.
func BuildOffer(info Info, nonce string) ckframework.DiscoveryOffer {
	return ckframework.DiscoveryOffer{
		CK:       ckframework.DiscoveryProtocolVersion,
		Type:     "offer",
		Nonce:    nonce,
		ServerID: info.ServerID,
		Name:     info.Name,
		AppName:  info.AppName,
		Port:     info.HTTPPort,
		Protocol: info.ProtocolVersion,
		Version:  info.Version,
		Methods:  info.Methods,
	}
}

// EncodeOffer serializes an offer and enforces the packet size cap.
func EncodeOffer(offer ckframework.DiscoveryOffer) ([]byte, error) {
	if offer.CK == 0 {
		offer.CK = ckframework.DiscoveryProtocolVersion
	}
	if offer.Type == "" {
		offer.Type = "offer"
	}
	data, err := json.Marshal(offer)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPacketSize {
		return nil, fmt.Errorf("discovery offer exceeds %d bytes", maxPacketSize)
	}
	return data, nil
}

// DecodeProbe parses and validates a UDP probe.
func DecodeProbe(data []byte) (ckframework.DiscoveryProbe, error) {
	var probe ckframework.DiscoveryProbe
	if len(data) == 0 || len(data) > maxPacketSize {
		return probe, ErrInvalidPacket
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return probe, fmt.Errorf("%w: %v", ErrInvalidPacket, err)
	}
	if probe.CK != ckframework.DiscoveryProtocolVersion || probe.Type != "probe" {
		return probe, ErrInvalidPacket
	}
	return probe, nil
}

// DecodeOffer parses and validates a UDP offer.
func DecodeOffer(data []byte) (ckframework.DiscoveryOffer, error) {
	var offer ckframework.DiscoveryOffer
	if len(data) == 0 || len(data) > maxPacketSize {
		return offer, ErrInvalidPacket
	}
	if err := json.Unmarshal(data, &offer); err != nil {
		return offer, fmt.Errorf("%w: %v", ErrInvalidPacket, err)
	}
	if offer.CK != ckframework.DiscoveryProtocolVersion || offer.Type != "offer" || offer.ServerID == "" || offer.Port <= 0 {
		return offer, ErrInvalidPacket
	}
	return offer, nil
}

// SanitizeName makes a name safe for mDNS instance labels and SSDP headers.
func SanitizeName(name string, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case '.', '\\', '/', '\n', '\r', '\t':
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "CoreKiosk"
	}
	if len(name) > 48 {
		name = strings.TrimSpace(name[:48])
	}
	return name
}

// IsPrivateSource accepts loopback, RFC1918, link-local, and ULA sources. Discovery
// is a LAN feature; refusing public sources avoids being used as an amplifier.
func IsPrivateSource(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return true
	}
	return false
}
