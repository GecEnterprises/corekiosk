package ckdiscovery

import (
	"context"
	"fmt"
	"sync"
)

type subAdvertiser interface {
	Start(ctx context.Context) error
	Update(info Info)
	Close() error
}

// Advertiser owns the enabled LAN discovery transports for one kiosk server.
// It is safe for concurrent use and keeps running when an individual transport
// fails to bind (the failure is reported through Status).
type Advertiser struct {
	cfg Config

	mu       sync.RWMutex
	info     Info
	started  bool
	active   []string
	errors   map[string]string
	subs     map[string]subAdvertiser
	ctx      context.Context
	cancel   context.CancelFunc
	closeOne sync.Once
}

// New creates an advertiser. Call Start to bind transports.
func New(cfg Config) *Advertiser {
	return &Advertiser{
		cfg:    cfg,
		errors: map[string]string{},
		subs:   map[string]subAdvertiser{},
	}
}

// Start binds the configured transports. Individual bind failures are recorded in
// Status and do not stop the other transports. The returned error is non-nil only
// when every configured transport failed.
func (a *Advertiser) Start(ctx context.Context, info Info) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.info = info
	if len(a.info.Methods) == 0 {
		a.info.Methods = a.cfg.normalizedMethods()
	}
	methods := a.cfg.normalizedMethods()
	a.started = true
	cfg := a.cfg
	a.mu.Unlock()

	getInfo := func() Info {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.info
	}

	for _, method := range methods {
		var sub subAdvertiser
		switch method {
		case MethodUDP:
			sub = newUDPResponder(cfg.udpPort(), getInfo)
		case MethodMDNS:
			sub = newMDNSAdvertiser(cfg, getInfo)
		case MethodSSDP:
			sub = newSSDPAdvertiser(cfg, getInfo)
		default:
			continue
		}
		if err := sub.Start(a.ctx); err != nil {
			a.mu.Lock()
			a.errors[method] = err.Error()
			a.mu.Unlock()
			continue
		}
		a.mu.Lock()
		a.subs[method] = sub
		a.active = append(a.active, method)
		a.mu.Unlock()
	}

	a.mu.RLock()
	active := len(a.active)
	a.mu.RUnlock()
	if active == 0 {
		return fmt.Errorf("no discovery transports could start: %v", a.errorStrings())
	}
	return nil
}

// Update applies new identity/address/port data without changing the configured
// transports. mDNS and SSDP re-announce; UDP reads the new Info per request.
func (a *Advertiser) Update(info Info) {
	a.mu.Lock()
	if len(info.Methods) == 0 {
		info.Methods = a.cfg.normalizedMethods()
	}
	a.info = info
	subs := make([]subAdvertiser, 0, len(a.subs))
	for _, sub := range a.subs {
		subs = append(subs, sub)
	}
	a.mu.Unlock()

	for _, sub := range subs {
		sub.Update(info)
	}
}

// Status returns a copy of the current advertiser state.
func (a *Advertiser) Status() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	errors := map[string]string{}
	for k, v := range a.errors {
		errors[k] = v
	}
	return Status{
		Enabled:   a.started,
		Name:      a.info.Name,
		Methods:   append([]string(nil), a.info.Methods...),
		Active:    append([]string(nil), a.active...),
		UDPPort:   a.cfg.udpPort(),
		Port:      a.info.HTTPPort,
		Addresses: append([]string(nil), a.info.Addresses...),
		Errors:    errors,
	}
}

// Close stops all transports and sends mDNS/SSDP goodbye notifications.
func (a *Advertiser) Close() error {
	a.closeOne.Do(func() {
		a.mu.Lock()
		if a.cancel != nil {
			a.cancel()
		}
		subs := make([]subAdvertiser, 0, len(a.subs))
		for _, sub := range a.subs {
			subs = append(subs, sub)
		}
		a.subs = map[string]subAdvertiser{}
		a.active = nil
		a.mu.Unlock()

		for _, sub := range subs {
			_ = sub.Close()
		}
	})
	return nil
}

func (a *Advertiser) errorStrings() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.errors))
	for method, err := range a.errors {
		out = append(out, method+": "+err)
	}
	return out
}
