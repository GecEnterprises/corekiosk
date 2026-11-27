package ckserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver/store"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/gin-gonic/gin"
)

// Option configures a corekiosk Server instance.
type Option func(*configOptions)

type configOptions struct {
	dbPath             string
	keyPath            string
	defaultOpUser      string
	defaultOpPass      string
	operatorTokenTTL   time.Duration
	bridgeToken        string
	launcherVersion    string
	gameVersion        string
	telemetryIngestURL string
	telemetryAPIKey    string
	devMode            bool
}

func WithDatabase(path string) Option            { return func(o *configOptions) { o.dbPath = path } }
func WithKeyFile(path string) Option             { return func(o *configOptions) { o.keyPath = path } }
func WithDefaultOperator(user, pass string) Option { return func(o *configOptions) { o.defaultOpUser = user; o.defaultOpPass = pass } }
func WithOperatorTokenTTL(d time.Duration) Option { return func(o *configOptions) { o.operatorTokenTTL = d } }
func WithBridgeToken(tok string) Option          { return func(o *configOptions) { o.bridgeToken = tok } }
func WithVersions(launcher, game string) Option  { return func(o *configOptions) { o.launcherVersion = launcher; o.gameVersion = game } }
func WithTelemetryIngest(url, apiKey string) Option {
	return func(o *configOptions) { o.telemetryIngestURL = url; o.telemetryAPIKey = apiKey }
}
func WithDevMode(v bool) Option { return func(o *configOptions) { o.devMode = v } }

// Server is the reusable embeddable corekiosk HTTP + WebSocket server.
type Server struct {
	spec         *ckframework.AppSpec
	store        *store.Store
	identity     *ServerIdentity
	bootID       string
	bridgeToken  string
	devMode      bool
	deviceAuth   *DeviceAuth
	operatorAuth *OperatorAuth
	outbox       *cktelemetry.Outbox
	shipper      *cktelemetry.Shipper
	hub          *Hub
	router       *gin.Engine
	listener     *listenerManager
	companionLn  *listenerManager
	companionDir string
}

func New(spec *ckframework.AppSpec, opts ...Option) (*Server, error) {
	cfg := configOptions{
		dbPath:           "./data/kiosk.db",
		keyPath:          "./data/server.key",
		defaultOpUser:    "operator",
		defaultOpPass:    "operator",
		operatorTokenTTL: 30 * time.Minute,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	st, err := store.Open(cfg.dbPath)
	if err != nil {
		return nil, err
	}

	identity, err := LoadOrGenerateIdentity(cfg.keyPath)
	if err != nil {
		_ = st.Close()
		return nil, err
	}

	bootBytes := make([]byte, 6)
	_, _ = rand.Read(bootBytes)
	bootID := hex.EncodeToString(bootBytes)

	bridgeTok := cfg.bridgeToken
	if bridgeTok == "" {
		bridgeTok = os.Getenv("CK_BRIDGE_TOKEN")
	}
	if bridgeTok == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		bridgeTok = hex.EncodeToString(b)
	}
	if !cfg.devMode && os.Getenv("CK_DEV") == "1" {
		cfg.devMode = true
	}

	// Register this kiosk PC server as a first-class row in `devices` for Telemetry Explorer joins.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "Kiosk PC"
	}
	now := time.Now().UnixMilli()
	for _, sa := range spec.SeedAssets() {
		if len(sa.Data) > 0 {
			if _, err := st.PutAsset(context.Background(), sa.Filename, sa.Key, sa.Data, now); err != nil {
				_ = st.Close()
				return nil, err
			}
		}
	}
	_ = st.UpsertDevice(context.Background(), ckframework.DeviceRecord{
		DeviceID:         identity.ServerID,
		PublicKey:        identity.PubBase64,
		Role:             "server",
		DeviceName:       hostname,
		DeviceType:       runtime.GOOS + "_pc",
		OSName:           runtime.GOOS,
		OSVersion:        runtime.GOARCH,
		AppVersion:       spec.Version,
		FrameworkVersion: ckframework.Version,
		LauncherVersion:  cfg.launcherVersion,
		GameVersion:      cfg.gameVersion,
		ProtocolVersion:  ckframework.ProtocolVersion,
		AllowCloudRelay:  true,
		LastIP:           LocalIPv4(),
		PairedAtMs:       now,
		LastSeenAtMs:     now,
	})
	_ = st.InterruptOpenRounds(context.Background(), now)

	outbox := cktelemetry.NewOutbox(st.DB(), identity.ServerID, "server", bootID)
	ingestURL := strings.TrimSpace(cfg.telemetryIngestURL)
	apiKey := strings.TrimSpace(cfg.telemetryAPIKey)
	flushSec := 15
	batchSize := 100
	if storedTel, ok, err := st.LoadTelemetryConfig(context.Background()); err == nil && ok {
		if storedTel.IngestURL != "" {
			ingestURL = storedTel.IngestURL
		}
		if storedTel.APIKey != "" {
			apiKey = storedTel.APIKey
		}
		flushSec = storedTel.FlushIntervalSec
		batchSize = storedTel.BatchSize
	}
	if ingestURL != "" || apiKey != "" {
		_, _ = st.SaveTelemetryConfig(context.Background(), store.TelemetryConfigRecord{
			IngestURL:        ingestURL,
			APIKey:           apiKey,
			FlushIntervalSec: flushSec,
			BatchSize:        batchSize,
		}, now)
	}
	shipper := cktelemetry.NewShipper(outbox, cktelemetry.ShipperConfig{
		IngestURL:      ingestURL,
		InstallationID: identity.ServerID,
		AppName:        spec.Name,
		DeviceID:       identity.ServerID,
		PrivateKey:     identity.PrivateKey,
		APIKey:         apiKey,
		FlushInterval:  time.Duration(flushSec) * time.Second,
		BatchSize:      batchSize,
	})

	devAuth := NewDeviceAuth(identity, st)
	opAuth, err := NewOperatorAuth(identity, st, cfg.defaultOpUser, cfg.defaultOpPass, cfg.operatorTokenTTL)
	if err != nil {
		_ = st.Close()
		return nil, err
	}

	hub, err := NewHub(spec, st, outbox, identity.ServerID, bootID, cfg.launcherVersion, cfg.gameVersion)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	hub.SetCloudOnlineFunc(shipper.CloudOnline)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	srv := &Server{
		spec:         spec,
		store:        st,
		identity:     identity,
		bootID:       bootID,
		bridgeToken:  bridgeTok,
		devMode:      cfg.devMode,
		deviceAuth:   devAuth,
		operatorAuth: opAuth,
		outbox:       outbox,
		shipper:      shipper,
		hub:          hub,
		router:       router,
	}
	srv.listener = newListenerManager(srv, "api", router, true)
	hub.SetPresenceInfoFunc(func() (string, string) {
		st := srv.ListenerStatus()
		addr := ""
		if len(st.Addresses) > 0 {
			addr = st.Addresses[0]
		} else if st.Port != "" {
			addr = LocalAddresses(st.Port)[0]
		}
		return addr, devAuth.PairCode()
	})
	devAuth.SetOnRotate(hub.NotifyGamePresence)
	hub.RegisterActionHandler("server.relisten", func(_ context.Context, _ map[string]any, _ *OperatorClaims) (ckframework.ActionResult, error) {
		if err := srv.RestartListener("operator_action"); err != nil {
			return ckframework.ActionResult{}, err
		}
		st := srv.ListenerStatus()
		return ckframework.ActionResult{
			OK:      true,
			Action:  "server.relisten",
			Message: "Listener rebound on " + st.BoundAddr,
			Data: map[string]any{
				"bound_addr": st.BoundAddr,
				"addresses":  st.Addresses,
				"generation": st.Generation,
			},
		}, nil
	})
	srv.registerRoutes(router)
	return srv, nil
}

func (s *Server) Spec() *ckframework.AppSpec     { return s.spec }
func (s *Server) Store() *store.Store            { return s.store }
func (s *Server) Identity() *ServerIdentity      { return s.identity }
func (s *Server) DeviceAuth() *DeviceAuth        { return s.deviceAuth }
func (s *Server) OperatorAuth() *OperatorAuth    { return s.operatorAuth }
func (s *Server) Outbox() *cktelemetry.Outbox    { return s.outbox }
func (s *Server) Shipper() *cktelemetry.Shipper  { return s.shipper }
func (s *Server) Hub() *Hub                      { return s.hub }

// TelemetryConfig returns the active telemetry shipper configuration.
func (s *Server) TelemetryConfig() store.TelemetryConfigRecord {
	cfg := s.shipper.Config()
	sec := int(cfg.FlushInterval / time.Second)
	if sec <= 0 {
		sec = 15
	}
	bs := cfg.BatchSize
	if bs <= 0 {
		bs = 100
	}
	return store.TelemetryConfigRecord{
		IngestURL:        cfg.IngestURL,
		APIKey:           cfg.APIKey,
		FlushIntervalSec: sec,
		BatchSize:        bs,
	}
}

// StoreTelemetryConfigFromParams constructs a TelemetryConfigRecord without requiring callers to import store.
func (s *Server) StoreTelemetryConfigFromParams(ingestURL, apiKey string, flushIntervalSec, batchSize int) store.TelemetryConfigRecord {
	return store.TelemetryConfigRecord{
		IngestURL:        ingestURL,
		APIKey:           apiKey,
		FlushIntervalSec: flushIntervalSec,
		BatchSize:        batchSize,
	}
}

// UpdateTelemetryConfig persists the telemetry parameters in SQLite and hot-reloads the background shipper.
func (s *Server) UpdateTelemetryConfig(ctx context.Context, rec store.TelemetryConfigRecord) (store.TelemetryConfigRecord, error) {
	saved, err := s.store.SaveTelemetryConfig(ctx, rec, time.Now().UnixMilli())
	if err != nil {
		return saved, err
	}
	s.shipper.UpdateConfig(
		saved.IngestURL,
		saved.APIKey,
		time.Duration(saved.FlushIntervalSec)*time.Second,
		saved.BatchSize,
	)
	return saved, nil
}

// TestTelemetryConnection sends an Ed25519-signed test ping batch to the given (or configured) ingest URL.
func (s *Server) TestTelemetryConnection(ctx context.Context, ingestURL, apiKey string) (cktelemetry.TestResult, error) {
	return s.shipper.TestConnection(ctx, ingestURL, apiKey)
}

// StoreLauncherConfigFromParams constructs a LauncherConfigRecord without requiring callers to import store.
func (s *Server) StoreLauncherConfigFromParams(autoLaunch bool, countdownSeconds int, autoRestartOnExit, openCompanion bool) store.LauncherConfigRecord {
	return store.LauncherConfigRecord{
		AutoLaunch:        autoLaunch,
		CountdownSeconds:  countdownSeconds,
		AutoRestartOnExit: autoRestartOnExit,
		OpenCompanion:     openCompanion,
	}
}

// LauncherConfig loads the persisted launcher startup configuration (or seeds defaults on first boot).
func (s *Server) LauncherConfig(ctx context.Context, defaults store.LauncherConfigRecord) store.LauncherConfigRecord {
	if rec, ok, err := s.store.LoadLauncherConfig(ctx); err == nil && ok {
		return rec
	}
	saved, err := s.store.SaveLauncherConfig(ctx, defaults, time.Now().UnixMilli())
	if err != nil {
		return defaults
	}
	return saved
}

// UpdateLauncherConfig persists updated launcher startup preferences to SQLite.
func (s *Server) UpdateLauncherConfig(ctx context.Context, rec store.LauncherConfigRecord) (store.LauncherConfigRecord, error) {
	return s.store.SaveLauncherConfig(ctx, rec, time.Now().UnixMilli())
}
func (s *Server) Router() *gin.Engine            { return s.router }
func (s *Server) BridgeToken() string            { return s.bridgeToken }
func (s *Server) ListenerStatus() ListenerStatus { return s.listener.Status() }
func (s *Server) CompanionDir() string           { return s.companionDir }

// CompanionStatus returns the live listener status of the bundled React Native Web host (if started).
func (s *Server) CompanionStatus() ListenerStatus {
	if s.companionLn == nil {
		return ListenerStatus{}
	}
	return s.companionLn.Status()
}

// StartCompanionWeb hosts a compiled React Native Web static bundle on its own self-healing
// all-interface port (e.g. ":8081") alongside the API server.
func (s *Server) StartCompanionWeb(ctx context.Context, addr string, dir string) (string, error) {
	handler, err := NewCompanionWebHandler(dir)
	if err != nil {
		return "", err
	}
	s.companionDir = dir
	s.companionLn = newListenerManager(s, "companion", handler, false)
	return s.companionLn.start(ctx, addr)
}

// RestartListener cleanly rebinds the HTTP/WebSocket listener (and Companion Web listener if active)
// on all interfaces and drops stale remote connections while keeping local loopback bridge sessions intact.
func (s *Server) RestartListener(reason string) error {
	if reason == "" {
		reason = "manual"
	}
	err := s.listener.restart(reason, true)
	if s.companionLn != nil {
		if cErr := s.companionLn.restart(reason, true); cErr != nil && err == nil {
			err = cErr
		}
	}
	return err
}

func (s *Server) Start(ctx context.Context, addr string) (string, error) {
	bound, err := s.listener.start(ctx, addr)
	if err != nil {
		return "", err
	}
	go s.shipper.Run(ctx)
	cktelemetry.StartHostCollector(ctx, s.outbox, 3*time.Second, func(info ckframework.WifiInfo) {
		s.hub.SetWifiInfo(info)
	})
	return bound, nil
}

func (s *Server) Close(ctx context.Context) error {
	if s.companionLn != nil {
		s.companionLn.close(ctx)
	}
	if s.listener != nil {
		s.listener.close(ctx)
	}
	return s.store.Close()
}

// LocalIPv4s returns all active, non-loopback, non-link-local IPv4 addresses on this host,
// ordered with the primary outbound LAN route first and virtual adapters last.
func LocalIPv4s() []string {
	primary := primaryOutboundIPv4()

	type candidate struct {
		ip       string
		priority int
	}
	var cands []candidate
	seen := map[string]struct{}{}

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			nameLower := strings.ToLower(iface.Name)
			isVirtual := strings.Contains(nameLower, "vethernet") ||
				strings.Contains(nameLower, "wsl") ||
				strings.Contains(nameLower, "hyper-v") ||
				strings.Contains(nameLower, "vmware") ||
				strings.Contains(nameLower, "virtualbox") ||
				strings.Contains(nameLower, "docker") ||
				strings.Contains(nameLower, "tailscale") ||
				strings.Contains(nameLower, "zerotier")

			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipnet.IP.To4()
				if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() || ip4.IsUnspecified() {
					continue
				}
				ipStr := ip4.String()
				if _, dup := seen[ipStr]; dup {
					continue
				}
				seen[ipStr] = struct{}{}

				prio := 2
				if ipStr == primary {
					prio = 0
				} else if !isVirtual && ip4.IsPrivate() {
					prio = 1
				} else if isVirtual {
					prio = 3
				}
				cands = append(cands, candidate{ip: ipStr, priority: prio})
			}
		}
	}

	slices.SortFunc(cands, func(a, b candidate) int {
		if a.priority != b.priority {
			return a.priority - b.priority
		}
		return strings.Compare(a.ip, b.ip)
	})

	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.ip)
	}
	return out
}

// LocalAddresses formats all active LAN IPv4 addresses as ip:port.
func LocalAddresses(port string) []string {
	ips := LocalIPv4s()
	if len(ips) == 0 {
		return []string{"127.0.0.1:" + port}
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, port))
	}
	return out
}

// LocalIPv4 returns the best non-loopback, non-link-local LAN IPv4 address for booth display.
func LocalIPv4() string {
	ips := LocalIPv4s()
	if len(ips) > 0 {
		return ips[0]
	}
	return "127.0.0.1"
}

func primaryOutboundIPv4() string {
	// UDP dial never sends packets; OS routing table selects the preferred local IPv4.
	conn, err := net.Dial("udp4", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if la, ok := conn.LocalAddr().(*net.UDPAddr); ok && la.IP != nil {
		if ip4 := la.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() && !ip4.IsLoopback() {
			return ip4.String()
		}
	}
	return ""
}
