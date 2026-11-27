package ckdiscovery

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/grandcat/zeroconf"
)

type mdnsAdvertiser struct {
	cfg     Config
	getInfo func() Info

	mu     sync.Mutex
	server *zeroconf.Server
	key    string
}

func newMDNSAdvertiser(cfg Config, getInfo func() Info) *mdnsAdvertiser {
	return &mdnsAdvertiser{cfg: cfg, getInfo: getInfo}
}

func (m *mdnsAdvertiser) Start(context.Context) error {
	return m.register(m.getInfo())
}

func (m *mdnsAdvertiser) Update(info Info) {
	key := mdnsKey(info)
	m.mu.Lock()
	changed := key != m.key
	m.mu.Unlock()
	if changed {
		_ = m.register(info)
	}
}

func (m *mdnsAdvertiser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server != nil {
		m.server.Shutdown()
		m.server = nil
	}
	return nil
}

func (m *mdnsAdvertiser) register(info Info) error {
	if info.HTTPPort <= 0 || len(info.Addresses) == 0 {
		return fmt.Errorf("mdns requires a port and at least one LAN address")
	}
	name := SanitizeName(info.Name, info.AppName)
	short := info.ServerID
	if len(short) > 8 {
		short = short[:8]
	}
	instance := name
	if short != "" {
		instance = fmt.Sprintf("%s [%s]", name, short)
	}
	host := "corekiosk-" + short
	if short == "" {
		host = "corekiosk"
	}
	text := []string{
		"id=" + info.ServerID,
		"app=" + info.AppName,
		"proto=" + fmt.Sprintf("%d", info.ProtocolVersion),
		"path=/v1/health",
		"v=" + info.Version,
		"caps=" + strings.Join(info.Methods, ","),
	}

	server, err := zeroconf.RegisterProxy(
		instance,
		strings.TrimSuffix(ckframework.DiscoveryServiceType, "."),
		"local.",
		info.HTTPPort,
		host,
		info.Addresses,
		text,
		nil,
	)
	if err != nil {
		return err
	}

	m.mu.Lock()
	old := m.server
	m.server = server
	m.key = mdnsKey(info)
	m.mu.Unlock()
	if old != nil {
		old.Shutdown()
	}
	return nil
}

func mdnsKey(info Info) string {
	return fmt.Sprintf("%s|%d|%s|%s", info.Name, info.HTTPPort, strings.Join(info.Addresses, ","), info.ServerID)
}
