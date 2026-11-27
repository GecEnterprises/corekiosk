package ckframework

// LAN discovery constants and public wire types. Discovery is an out-of-band
// convenience layer: it never replaces the pinned Ed25519 identity or the
// six-digit pairing flow.
const (
	// DiscoveryProtocolVersion is the version of the UDP probe/offer envelope.
	DiscoveryProtocolVersion = 1
	// DiscoveryServiceType is the DNS-SD service used by mDNS.
	DiscoveryServiceType = "_corekiosk._tcp"
	// DiscoverySSDPType is the SSDP notification/search target.
	DiscoverySSDPType = "urn:gec:corekiosk:server:1"
	// DiscoveryDefaultUDPPort is the default UDP probe/offer port.
	DiscoveryDefaultUDPPort = 8099
)

// DiscoveryInfo is the public, non-sensitive discovery metadata exposed through
// /v1/health and the launcher UI. It intentionally contains no pairing code and
// no secrets.
type DiscoveryInfo struct {
	Enabled         bool     `json:"enabled"`
	Name            string   `json:"name,omitempty"`
	AppName         string   `json:"app_name,omitempty"`
	Port            int      `json:"port,omitempty"`
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	Version         string   `json:"version,omitempty"`
	Methods         []string `json:"methods,omitempty"`
	UDPPort         int      `json:"udp_port,omitempty"`
	ServiceType     string   `json:"service_type,omitempty"`
	SSDPType        string   `json:"ssdp_type,omitempty"`
	Addresses       []string `json:"addresses,omitempty"`
}

// DiscoveryProbe is sent by a companion to discover kiosk servers on the LAN.
type DiscoveryProbe struct {
	CK       int    `json:"ck"`
	Type     string `json:"t"`
	Nonce    string `json:"n"`
	Protocol int    `json:"p,omitempty"`
}

// DiscoveryOffer is the unicast reply to a DiscoveryProbe. It only carries enough
// information to reach /v1/health; identity is verified there.
type DiscoveryOffer struct {
	CK       int      `json:"ck"`
	Type     string   `json:"t"`
	Nonce    string   `json:"n,omitempty"`
	ServerID string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	AppName  string   `json:"app,omitempty"`
	Port     int      `json:"port"`
	Protocol int      `json:"proto"`
	Version  string   `json:"v,omitempty"`
	Methods  []string `json:"caps,omitempty"`
}
