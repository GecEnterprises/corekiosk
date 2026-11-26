package cklauncher

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cklauncher/winautostart"
	"github.com/GecEnterprises/corekiosk/go/ckserver"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
)

// Options configures the kiosk supervisor & native golang-gui/goui pre-launch UI.
type Options struct {
	ListenAddr        string
	GameBinary        string
	GameArgs          []string
	ManifestPath      string
	CountdownSeconds  int
	AutoRestartOnExit bool
	Headless          bool
}

// Run starts ckserver, runs the pre-launch countdown + native goui window (with
// Operator credentials editor, Settings/Actions, Paired Devices, Boards & Telemetry Explorer),
// and supervises the child game binary until shutdown.
func Run(srv *ckserver.Server, opts Options) error {
	installAuto := flag.Bool("install-autostart", false, "Install Windows logon Task Scheduler autostart and exit")
	uninstallAuto := flag.Bool("uninstall-autostart", false, "Remove Windows logon autostart task and exit")
	launchNow := flag.Bool("now", false, "Skip the pre-launch countdown")
	noGame := flag.Bool("no-game", false, "Run server + launcher UI without spawning the game binary")
	headlessFlag := flag.Bool("headless", false, "Run without opening the native goui desktop window")
	listenFlag := flag.String("listen", "", "Override HTTP listen address (e.g. :8080)")
	flag.Parse()

	if *installAuto {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := winautostart.Install(srv.Spec().Name, exe); err != nil {
			return err
		}
		fmt.Printf("Installed Windows logon autostart for %s (%s)\n", srv.Spec().Name, exe)
		return nil
	}
	if *uninstallAuto {
		if err := winautostart.Uninstall(srv.Spec().Name); err != nil {
			return err
		}
		fmt.Printf("Removed Windows logon autostart for %s\n", srv.Spec().Name)
		return nil
	}

	if *listenFlag != "" {
		opts.ListenAddr = *listenFlag
	}
	if opts.ListenAddr == "" {
		opts.ListenAddr = ":8080"
	}
	if opts.CountdownSeconds == 0 {
		opts.CountdownSeconds = 30
	}
	if *headlessFlag {
		opts.Headless = true
	}

	// Verify packaged game manifest hash if present.
	if opts.ManifestPath != "" {
		if m, err := LoadManifest(opts.ManifestPath); err == nil {
			if opts.GameBinary == "" {
				opts.GameBinary = m.Game.Binary
			}
			if m.Game.SHA256 != "" && opts.GameBinary != "" {
				if actual, err := HashFile(opts.GameBinary); err == nil && actual != m.Game.SHA256 {
					_, _ = srv.Outbox().Record(context.Background(), cktelemetry.Event{
						Category: "launcher",
						Kind:     "version.game_binary_changed",
						Severity: "warn",
						Payload: map[string]any{
							"expected_sha256": m.Game.SHA256,
							"actual_sha256":   actual,
						},
					})
				}
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	boundAddr, err := srv.Start(ctx, opts.ListenAddr)
	if err != nil {
		return err
	}
	defer srv.Close(context.Background())

	_, port, _ := net.SplitHostPort(boundAddr)
	localURL := fmt.Sprintf("http://127.0.0.1:%s", port)
	lanAddr := fmt.Sprintf("%s:%s", ckserver.LocalIPv4(), port)

	cdSecs := opts.CountdownSeconds
	if *launchNow {
		cdSecs = 0
	}
	disableGame := *noGame || opts.GameBinary == ""
	cd := NewCountdown(cdSecs, disableGame)
	sup := NewSupervisor(opts.GameBinary, opts.GameArgs, localURL, srv.BridgeToken(), opts.AutoRestartOnExit, srv.Outbox())

	// Wire launcher-targeted actions into ckserver Hub.
	srv.Hub().RegisterActionHandler("launcher.restart_game", func(_ context.Context, _ map[string]any, _ *ckserver.OperatorClaims) (ckframework.ActionResult, error) {
		cd.LaunchNow()
		sup.Restart()
		return ckframework.ActionResult{OK: true, Action: "launcher.restart_game", Message: "Game restart triggered."}, nil
	})

	_, _ = srv.Outbox().Record(ctx, cktelemetry.Event{
		Category: "launcher",
		Kind:     "launcher.boot",
		Severity: "info",
		Payload: map[string]any{
			"launcher_version":  FullVersion(),
			"framework_version": ckframework.Version,
			"app_name":          srv.Spec().Name,
			"app_version":       srv.Spec().Version,
			"lan_address":       lanAddr,
		},
	})

	hint := srv.OperatorAuth().BoothHint(ctx)
	slog.Info("corekiosk launcher ready",
		"app", srv.Spec().Name,
		"lan", "http://"+lanAddr,
		"pair_code", srv.DeviceAuth().PairCode(),
		"operator_user", hint.Username,
		"operator_default", hint.IsDefault,
	)

	if !disableGame {
		go func() {
			if cd.Wait(ctx) {
				sup.Run(ctx)
			}
		}()
	}

	if opts.Headless {
		<-ctx.Done()
		sup.Stop()
		return nil
	}

	// Native golang-gui/goui event loop runs on the main OS thread.
	err = RunNativeUI(ctx, srv, cd, sup, lanAddr)
	stop()
	sup.Stop()
	return err
}
