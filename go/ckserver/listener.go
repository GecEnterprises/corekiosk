package ckserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
)

// ListenerStatus describes the live HTTP/WS listener state and active host interfaces.
type ListenerStatus struct {
	Listening    bool     `json:"listening"`
	Rebinding    bool     `json:"rebinding"`
	ListenAddr   string   `json:"listen_addr"`
	BoundAddr    string   `json:"bound_addr"`
	Port         string   `json:"port"`
	LANOnline    bool     `json:"lan_online"`
	Addresses    []string `json:"addresses"`
	Generation   int      `json:"generation"`
	LastBindAtMs int64    `json:"last_bind_at_ms"`
	LastReason   string   `json:"last_reason,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
}

// trackedListener wraps net.Listener to track active client TCP connections so dead or
// orphaned non-loopback connections can be pruned immediately when an adapter or IP drops,
// without ever disturbing loopback (127.0.0.1) game bridge traffic.
type trackedListener struct {
	net.Listener
	tracker *connTracker
}

func (tl *trackedListener) Accept() (net.Conn, error) {
	c, err := tl.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(10 * time.Second)
	}
	return tl.tracker.track(c), nil
}

type connTracker struct {
	mu    sync.Mutex
	conns map[*trackedConn]struct{}
}

func newConnTracker() *connTracker {
	return &connTracker{conns: make(map[*trackedConn]struct{})}
}

func (ct *connTracker) track(c net.Conn) *trackedConn {
	tc := &trackedConn{Conn: c, tracker: ct}
	ct.mu.Lock()
	ct.conns[tc] = struct{}{}
	ct.mu.Unlock()
	return tc
}

func (ct *connTracker) untrack(tc *trackedConn) {
	ct.mu.Lock()
	delete(ct.conns, tc)
	ct.mu.Unlock()
}

// pruneStaleLAN closes any open non-loopback connection whose local IPv4 address is no longer
// among validLANIPs (e.g. Wi-Fi disconnected, switched networks, or USB adapter unplugged).
// Loopback connections (127.0.0.1 / ::1) are always preserved so the local game bridge stays live.
func (ct *connTracker) pruneStaleLAN(validLANIPs []string) int {
	valid := make(map[string]struct{}, len(validLANIPs))
	for _, ip := range validLANIPs {
		valid[ip] = struct{}{}
	}

	var stale []*trackedConn
	ct.mu.Lock()
	for tc := range ct.conns {
		host, _, err := net.SplitHostPort(tc.LocalAddr().String())
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			if _, ok := valid[ip4.String()]; !ok {
				stale = append(stale, tc)
			}
		}
	}
	ct.mu.Unlock()

	for _, tc := range stale {
		_ = tc.Close()
	}
	return len(stale)
}

// closeAllRemote closes all non-loopback connections (used during manual/forced rebinds).
func (ct *connTracker) closeAllRemote() int {
	return ct.pruneStaleLAN(nil)
}

type trackedConn struct {
	net.Conn
	tracker *connTracker
	once    sync.Once
}

func (tc *trackedConn) Close() error {
	var err error
	tc.once.Do(func() {
		tc.tracker.untrack(tc)
		err = tc.Conn.Close()
	})
	return err
}

type listenerManager struct {
	srv            *Server
	name           string
	handler        http.Handler
	trackPrimaryIP bool
	ctx            context.Context
	cancel         context.CancelFunc
	tracker        *connTracker

	opMu      sync.Mutex // serializes Start / Restart / Close transitions
	mu        sync.RWMutex
	reqAddr   string
	bindHost  string
	port      string
	curLn     net.Listener
	curSrv    *http.Server
	serveDone chan struct{}
	status    ListenerStatus
}

func newListenerManager(srv *Server, name string, handler http.Handler, trackPrimaryIP bool) *listenerManager {
	return &listenerManager{
		srv:            srv,
		name:           name,
		handler:        handler,
		trackPrimaryIP: trackPrimaryIP,
		tracker:        newConnTracker(),
	}
}

func (lm *listenerManager) Status() ListenerStatus {
	lm.mu.RLock()
	st := lm.status
	port := lm.port
	lm.mu.RUnlock()

	ips := LocalIPv4s()
	st.LANOnline = len(ips) > 0
	if port != "" {
		st.Addresses = formatAddresses(ips, port)
	}
	return st
}

func formatAddresses(ips []string, port string) []string {
	if len(ips) == 0 {
		return []string{net.JoinHostPort("127.0.0.1", port)}
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, port))
	}
	return out
}

func (lm *listenerManager) start(parentCtx context.Context, addr string) (string, error) {
	lm.opMu.Lock()
	defer lm.opMu.Unlock()

	if strings.HasPrefix(addr, ":") {
		addr = "0.0.0.0" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", ckframework.ErrValidation.Wrap(err, "invalid listen address %q", addr)
	}
	if host == "" {
		host = "0.0.0.0"
		addr = net.JoinHostPort(host, port)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	lm.ctx = ctx
	lm.cancel = cancel
	lm.reqAddr = addr
	lm.bindHost = host
	lm.port = port

	bound, err := lm.bindLocked("initial_boot")
	if err != nil {
		cancel()
		return "", err
	}

	go lm.watchNetworkLoop(ctx)
	return bound, nil
}

func (lm *listenerManager) listenOnce(target string) (net.Listener, error) {
	lc := net.ListenConfig{
		KeepAlive: 10 * time.Second,
	}
	ln, err := lc.Listen(context.Background(), "tcp4", target)
	if err != nil {
		ln, err = lc.Listen(context.Background(), "tcp", target)
	}
	return ln, err
}

func (lm *listenerManager) bindLocked(reason string) (string, error) {
	target := net.JoinHostPort(lm.bindHost, lm.port)
	var ln net.Listener
	var err error

	// Retry briefly in case the OS is still releasing the socket from a previous bind/adapter reset.
	backoff := 80 * time.Millisecond
	for attempt := 0; attempt < 6; attempt++ {
		ln, err = lm.listenOnce(target)
		if err == nil {
			break
		}
		// If a specific LAN IP was requested and vanished because the adapter disconnected,
		// fall back to 0.0.0.0:<port> so the server stays alive on loopback & future interfaces.
		if lm.bindHost != "0.0.0.0" && lm.bindHost != "127.0.0.1" && lm.bindHost != "::" {
			wildcard := net.JoinHostPort("0.0.0.0", lm.port)
			if wln, werr := lm.listenOnce(wildcard); werr == nil {
				lm.bindHost = "0.0.0.0"
				ln = wln
				err = nil
				break
			}
		}
		if attempt < 5 {
			time.Sleep(backoff)
			if backoff < 500*time.Millisecond {
				backoff *= 2
			}
		}
	}
	if err != nil {
		lm.mu.Lock()
		lm.status.Listening = false
		lm.status.Rebinding = false
		lm.status.LastReason = reason
		lm.status.LastError = err.Error()
		lm.mu.Unlock()
		return "", ckframework.ErrInternal.Wrap(err, "listen on %s", target)
	}

	boundAddr := ln.Addr().String()
	if _, actualPort, splitErr := net.SplitHostPort(boundAddr); splitErr == nil && actualPort != "" {
		// Preserve resolved port if caller passed :0 so subsequent rebinds keep the same port!
		lm.port = actualPort
	}

	wrapped := &trackedListener{Listener: ln, tracker: lm.tracker}
	httpSrv := &http.Server{
		Handler:           lm.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan struct{})

	lm.mu.Lock()
	lm.curLn = wrapped
	lm.curSrv = httpSrv
	lm.serveDone = done
	lm.status.Generation++
	gen := lm.status.Generation
	lm.status.Listening = true
	lm.status.Rebinding = false
	lm.status.ListenAddr = net.JoinHostPort(lm.bindHost, lm.port)
	lm.status.BoundAddr = boundAddr
	lm.status.Port = lm.port
	lm.status.LastBindAtMs = time.Now().UnixMilli()
	lm.status.LastReason = reason
	lm.status.LastError = ""
	lm.mu.Unlock()

	go func(s *http.Server, l net.Listener, expectedGen int, doneCh chan struct{}) {
		serveErr := s.Serve(l)
		close(doneCh)

		if lm.ctx == nil || lm.ctx.Err() != nil || errors.Is(serveErr, http.ErrServerClosed) {
			return
		}

		// Unexpected listener failure (e.g. Winsock reset / adapter stack error): self-heal!
		lm.mu.RLock()
		stillCurrent := lm.status.Generation == expectedGen
		lm.mu.RUnlock()
		if !stillCurrent {
			return
		}

		lm.mu.Lock()
		lm.status.Listening = false
		lm.status.Rebinding = true
		if serveErr != nil {
			lm.status.LastError = serveErr.Error()
		}
		lm.mu.Unlock()

		if lm.srv.outbox != nil {
			errMsg := ""
			if serveErr != nil {
				errMsg = serveErr.Error()
			}
			_, _ = lm.srv.outbox.Record(context.Background(), cktelemetry.Event{
				Category: "net",
				Kind:     "net.listener_dropped",
				Severity: "error",
				Payload: map[string]any{
					"generation": expectedGen,
					"error":      errMsg,
				},
			})
		}

		go lm.recoverLoop("unexpected_serve_exit")
	}(httpSrv, wrapped, gen, done)

	return boundAddr, nil
}

func (lm *listenerManager) recoverLoop(reason string) {
	wait := 250 * time.Millisecond
	for {
		if lm.ctx == nil || lm.ctx.Err() != nil {
			return
		}
		if err := lm.restart(reason, false); err == nil {
			return
		}
		select {
		case <-lm.ctx.Done():
			return
		case <-time.After(wait):
			if wait < 3*time.Second {
				wait *= 2
			}
		}
	}
}

func (lm *listenerManager) restart(reason string, dropRemote bool) error {
	lm.opMu.Lock()
	defer lm.opMu.Unlock()

	if lm.ctx == nil || lm.ctx.Err() != nil {
		return ckframework.ErrConflict.New("server is shutting down")
	}

	lm.mu.Lock()
	oldLn := lm.curLn
	oldSrv := lm.curSrv
	oldDone := lm.serveDone
	lm.status.Rebinding = true
	lm.mu.Unlock()

	if oldLn != nil {
		_ = oldLn.Close()
	}
	if oldSrv != nil {
		// Close listening socket immediately without killing active loopback WS handlers unless dropRemote.
		_ = oldSrv.Close()
	}
	if dropRemote {
		lm.tracker.closeAllRemote()
	}
	if oldDone != nil {
		select {
		case <-oldDone:
		case <-time.After(500 * time.Millisecond):
		}
	}

	bound, err := lm.bindLocked(reason)
	if err != nil {
		return err
	}

	if lm.srv.outbox != nil {
		_, _ = lm.srv.outbox.Record(context.Background(), cktelemetry.Event{
			Category: "net",
			Kind:     "net.listener_restarted",
			Severity: "info",
			Payload: map[string]any{
				"reason":     reason,
				"bound_addr": bound,
				"addresses":  LocalAddresses(lm.port),
				"generation": lm.Status().Generation,
			},
		})
	}
	return nil
}

func (lm *listenerManager) close(ctx context.Context) {
	lm.opMu.Lock()
	defer lm.opMu.Unlock()

	if lm.cancel != nil {
		lm.cancel()
	}
	lm.mu.Lock()
	oldSrv := lm.curSrv
	oldLn := lm.curLn
	lm.status.Listening = false
	lm.status.Rebinding = false
	lm.mu.Unlock()

	if oldSrv != nil {
		_ = oldSrv.Shutdown(ctx)
	}
	if oldLn != nil {
		_ = oldLn.Close()
	}
	lm.tracker.closeAllRemote()
}

func (lm *listenerManager) probeReachable(ip string) bool {
	lm.mu.RLock()
	port := lm.port
	listening := lm.status.Listening
	lm.mu.RUnlock()
	if !listening || port == "" {
		return false
	}
	target := net.JoinHostPort(ip, port)
	c, err := net.DialTimeout("tcp4", target, 350*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (lm *listenerManager) watchNetworkLoop(ctx context.Context) {
	netChanges := cktelemetry.WatchNetworkChanges(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	prevIPs := LocalIPv4s()
	lastProbe := time.Now()

	check := func(fromEvent bool) {
		curIPs := LocalIPv4s()
		changed := !slices.Equal(prevIPs, curIPs)

		if changed {
			// Immediately sever half-open TCP/WS sockets bound to vanished local LAN IPs
			// while keeping 127.0.0.1 game bridge connections untouched.
			pruned := lm.tracker.pruneStaleLAN(curIPs)

			if lm.trackPrimaryIP {
				primaryIP := "127.0.0.1"
				if len(curIPs) > 0 {
					primaryIP = curIPs[0]
				}
				_ = lm.srv.store.TouchDevice(ctx, lm.srv.identity.ServerID, primaryIP, time.Now().UnixMilli())

				if lm.srv.outbox != nil {
					switch {
					case len(prevIPs) > 0 && len(curIPs) == 0:
						_, _ = lm.srv.outbox.Record(ctx, cktelemetry.Event{
							Category: "net",
							Kind:     "net.lan_disconnected",
							Severity: "warn",
							Payload: map[string]any{
								"prev_ips":     prevIPs,
								"pruned_conns": pruned,
							},
						})
					case len(prevIPs) == 0 && len(curIPs) > 0:
						_, _ = lm.srv.outbox.Record(ctx, cktelemetry.Event{
							Category: "net",
							Kind:     "net.lan_recovered",
							Severity: "info",
							Payload: map[string]any{
								"ips": curIPs,
							},
						})
					default:
						_, _ = lm.srv.outbox.Record(ctx, cktelemetry.Event{
							Category: "net",
							Kind:     "net.addresses_changed",
							Severity: "info",
							Payload: map[string]any{
								"prev_ips":     prevIPs,
								"ips":          curIPs,
								"pruned_conns": pruned,
							},
						})
					}
				}

				// Broadcast fresh snapshot & presence to connected clients/game so IP/Wi-Fi state stays in sync.
				lm.srv.hub.broadcastTablets(lm.srv.hub.Snapshot())
				lm.srv.hub.NotifyGamePresence()
			}
		}

		// Verify listener reachability whenever a new LAN IP appears, after OS network events,
		// or every 15s during steady state. If the wildcard socket wedged after an adapter reset,
		// automatically restart the listener!
		needProbe := changed || fromEvent || time.Since(lastProbe) >= 15*time.Second
		if needProbe {
			lastProbe = time.Now()
			probeIP := "127.0.0.1"
			if len(curIPs) > 0 {
				// Only probe primary LAN IP if bound to wildcard 0.0.0.0 or that exact IP.
				lm.mu.RLock()
				bh := lm.bindHost
				lm.mu.RUnlock()
				if bh == "0.0.0.0" || bh == "::" || bh == curIPs[0] {
					probeIP = curIPs[0]
				}
			}
			if !lm.probeReachable(probeIP) && ctx.Err() == nil {
				// Retry once after 150ms in case the new adapter's IP stack is still initializing.
				time.Sleep(150 * time.Millisecond)
				if !lm.probeReachable(probeIP) && ctx.Err() == nil {
					_ = lm.restart("adapter_probe_failed", false)
				}
			}
		}

		prevIPs = curIPs
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-netChanges:
			if !ok {
				netChanges = nil
				continue
			}
			check(true)
		case <-ticker.C:
			check(false)
		}
	}
}
