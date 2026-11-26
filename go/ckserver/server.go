package ckserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"runtime"
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
	httpSrv      *http.Server
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
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		bridgeTok = hex.EncodeToString(b)
	}

	// Register this kiosk PC server as a first-class row in `devices` for Telemetry Explorer joins.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "Kiosk PC"
	}
	now := time.Now().UnixMilli()
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
	shipper := cktelemetry.NewShipper(outbox, cktelemetry.ShipperConfig{
		IngestURL:      cfg.telemetryIngestURL,
		InstallationID: identity.ServerID,
		AppName:        spec.Name,
		DeviceID:       identity.ServerID,
		PrivateKey:     identity.PrivateKey,
		APIKey:         cfg.telemetryAPIKey,
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
	srv.registerRoutes(router)
	return srv, nil
}

func (s *Server) Spec() *ckframework.AppSpec  { return s.spec }
func (s *Server) Store() *store.Store         { return s.store }
func (s *Server) Identity() *ServerIdentity   { return s.identity }
func (s *Server) DeviceAuth() *DeviceAuth     { return s.deviceAuth }
func (s *Server) OperatorAuth() *OperatorAuth { return s.operatorAuth }
func (s *Server) Outbox() *cktelemetry.Outbox { return s.outbox }
func (s *Server) Shipper() *cktelemetry.Shipper { return s.shipper }
func (s *Server) Hub() *Hub                   { return s.hub }
func (s *Server) Router() *gin.Engine         { return s.router }
func (s *Server) BridgeToken() string         { return s.bridgeToken }

func (s *Server) Start(ctx context.Context, addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", ckframework.ErrInternal.Wrap(err, "listen on %s", addr)
	}
	s.httpSrv = &http.Server{Handler: s.router}
	go s.shipper.Run(ctx)
	cktelemetry.StartHostCollector(ctx, s.outbox, 10*time.Second, func(info ckframework.WifiInfo) {
		s.hub.SetWifiInfo(info)
	})
	go func() {
		_ = s.httpSrv.Serve(ln)
	}()
	return ln.Addr().String(), nil
}

func (s *Server) Close(ctx context.Context) error {
	if s.httpSrv != nil {
		_ = s.httpSrv.Shutdown(ctx)
	}
	return s.store.Close()
}

// LocalIPv4 returns the first non-loopback LAN IPv4 address for booth display.
func LocalIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}
