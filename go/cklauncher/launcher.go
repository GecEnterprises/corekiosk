package cklauncher

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cklauncher/winautostart"
	"github.com/GecEnterprises/corekiosk/go/ckserver"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/GecEnterprises/corekiosk/go/ckupdater"
)

// Options configures the kiosk supervisor, bundled Companion Web host, & native Fyne v2 UI.
type Options struct {
	ListenAddr        string
	CompanionAddr     string // Defaults to ":8090" when a companion web bundle is present
	CompanionDir      string // Static RN Web directory (auto-discovered from manifest, ./companion, or ../companion-rn/dist)
	OpenCompanion     bool   // Automatically open bundled Companion Web in browser on startup
	GameBinary        string
	GameArgs          []string
	ManifestPath      string
	CountdownSeconds  int
	AutoRestartOnExit bool
	Headless          bool
	UpdateURL         string
	UpdateAPIKey      string
	UpdateChannel     string
	TrustedUpdateKeys string // Comma-separated base64 Ed25519 release public keys
}

// Run starts ckserver (+ Companion Web host if bundled), runs the pre-launch countdown + native
// Fyne window, and supervises the child game binary until shutdown.
func Run(srv *ckserver.Server, opts Options) error {
	if handled, err := ckupdater.HandleApplyUpdateFlag(os.Args[1:]); handled {
		return err
	}
	installAuto := flag.Bool("install-autostart", false, "Install Windows logon Task Scheduler autostart and exit")
	uninstallAuto := flag.Bool("uninstall-autostart", false, "Remove Windows logon autostart task and exit")
	launchNow := flag.Bool("now", false, "Skip the pre-launch countdown")
	noGame := flag.Bool("no-game", false, "Run server + launcher UI without spawning the game binary")
	headlessFlag := flag.Bool("headless", false, "Run without opening the native Fyne desktop window")
	listenFlag := flag.String("listen", "", "Override HTTP API listen address (e.g. :8080)")
	companionListenFlag := flag.String("companion-listen", "", "Override bundled Companion Web listen address (default :8090)")
	companionDirFlag := flag.String("companion-dir", "", "Path to exported Companion Web static directory")
	noCompanionFlag := flag.Bool("no-companion", false, "Disable hosting the bundled Companion Web app")
	openCompanionFlag := flag.Bool("open-companion", false, "Open the bundled Companion Web app in a browser on startup")
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
	if *companionListenFlag != "" {
		opts.CompanionAddr = *companionListenFlag
	}
	if *companionDirFlag != "" {
		opts.CompanionDir = *companionDirFlag
	}
	if *openCompanionFlag {
		opts.OpenCompanion = true
	}
	if opts.CountdownSeconds == 0 {
		opts.CountdownSeconds = 30
	}
	if *headlessFlag {
		opts.Headless = true
	}

	baseDir := exeBaseDir()

	// Resolve packaged kiosk.manifest.json (relative to cwd or executable baseDir).
	var loadedManifest *Manifest
	manifestPath := resolveExistingPath(baseDir, opts.ManifestPath)
	if manifestPath != "" {
		if m, err := LoadManifest(manifestPath); err == nil {
			loadedManifest = m
			manifestDir := filepath.Dir(manifestPath)
			if opts.GameBinary == "" && m.Game.Binary != "" {
				opts.GameBinary = resolveRelative(manifestDir, m.Game.Binary)
			}
			if opts.CompanionDir == "" && m.Companion.Dir != "" {
				opts.CompanionDir = resolveRelative(manifestDir, m.Companion.Dir)
			}
			if opts.CompanionAddr == "" && m.Companion.Port > 0 {
				opts.CompanionAddr = fmt.Sprintf(":%d", m.Companion.Port)
			}
			if opts.UpdateURL == "" && m.Updates.URL != "" {
				opts.UpdateURL = m.Updates.URL
			}
			if opts.UpdateChannel == "" && m.Updates.Channel != "" {
				opts.UpdateChannel = m.Updates.Channel
			}
			if opts.TrustedUpdateKeys == "" && m.Updates.TrustedKeys != "" {
				opts.TrustedUpdateKeys = m.Updates.TrustedKeys
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

	if opts.CompanionAddr == "" {
		opts.CompanionAddr = ":8090"
	}
	if opts.CompanionDir == "" {
		opts.CompanionDir = discoverCompanionDir(baseDir)
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
	lanAddrs := ckserver.LocalAddresses(port)
	lanAddr := lanAddrs[0]

	var companionLocalURL string
	if !*noCompanionFlag && opts.CompanionDir != "" {
		compBound, compErr := srv.StartCompanionWeb(ctx, opts.CompanionAddr, opts.CompanionDir)
		if compErr != nil && *companionListenFlag == "" {
			// Fallback to an OS-assigned port if default :8090 is already bound.
			compBound, compErr = srv.StartCompanionWeb(ctx, ":0", opts.CompanionDir)
		}
		if compErr != nil {
			slog.Warn("companion web host skipped", "dir", opts.CompanionDir, "error", compErr)
		} else {
			_, compPort, _ := net.SplitHostPort(compBound)
			companionLocalURL = fmt.Sprintf("http://127.0.0.1:%s", compPort)
			slog.Info("companion web hosted",
				"listen", compBound,
				"dir", opts.CompanionDir,
				"urls", ckserver.LocalAddresses(compPort),
			)
		}
	}

	persistedLauncherCfg := srv.LauncherConfig(ctx, srv.StoreLauncherConfigFromParams(
		true,
		opts.CountdownSeconds,
		opts.AutoRestartOnExit,
		opts.OpenCompanion,
	))
	cdSecs := persistedLauncherCfg.CountdownSeconds
	if *launchNow {
		cdSecs = 0
	}
	disableGame := *noGame || opts.GameBinary == ""
	cd := NewCountdown(cdSecs, disableGame)
	if !persistedLauncherCfg.AutoLaunch && !*launchNow {
		cd.Pause()
	}
	autoRestart := persistedLauncherCfg.AutoRestartOnExit
	if *openCompanionFlag {
		persistedLauncherCfg.OpenCompanion = true
	}
	sup := NewSupervisor(opts.GameBinary, opts.GameArgs, localURL, srv.BridgeToken(), autoRestart, srv.Outbox())

	// Wire launcher-targeted actions into ckserver Hub.
	srv.Hub().RegisterActionHandler("launcher.restart_game", func(_ context.Context, _ map[string]any, _ *ckserver.OperatorClaims) (ckframework.ActionResult, error) {
		cd.LaunchNow()
		sup.Restart()
		return ckframework.ActionResult{OK: true, Action: "launcher.restart_game", Message: "Game restart triggered."}, nil
	})
	if companionLocalURL != "" {
		srv.Hub().RegisterActionHandler("launcher.open_companion", func(_ context.Context, _ map[string]any, _ *ckserver.OperatorClaims) (ckframework.ActionResult, error) {
			if err := OpenBrowser(companionLocalURL); err != nil {
				return ckframework.ActionResult{}, ckframework.ErrInternal.Wrap(err, "open browser")
			}
			return ckframework.ActionResult{OK: true, Action: "launcher.open_companion", Message: "Opened " + companionLocalURL}, nil
		})
	}

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
			"companion_url":     companionLocalURL,
		},
	})

	hint := srv.OperatorAuth().BoothHint(ctx)
	slog.Info("corekiosk launcher ready",
		"app", srv.Spec().Name,
		"listen", boundAddr,
		"addresses", lanAddrs,
		"companion", companionLocalURL,
		"pair_code", srv.DeviceAuth().PairCode(),
		"operator_user", hint.Username,
		"operator_default", hint.IsDefault,
	)

	if (opts.OpenCompanion || persistedLauncherCfg.OpenCompanion) && companionLocalURL != "" {
		_ = OpenBrowser(companionLocalURL)
	}

	if opts.UpdateURL == "" {
		opts.UpdateURL = os.Getenv("CK_UPDATES_URL")
	}
	if opts.UpdateAPIKey == "" {
		opts.UpdateAPIKey = os.Getenv("CK_TELEMETRY_API_KEY")
	}
	if opts.UpdateChannel == "" {
		opts.UpdateChannel = os.Getenv("CK_UPDATE_CHANNEL")
	}
	if opts.TrustedUpdateKeys == "" {
		opts.TrustedUpdateKeys = os.Getenv("CK_TRUSTED_UPDATE_KEYS")
	}

	var upd *ckupdater.Manager
	if opts.UpdateURL != "" && opts.TrustedUpdateKeys != "" {
		if trusted, err := ckupdater.ParseTrustedKeys(opts.TrustedUpdateKeys); err == nil && len(trusted) > 0 {
			upd = ckupdater.NewManager(ckupdater.Config{
				EndpointURL:    opts.UpdateURL,
				APIKey:         opts.UpdateAPIKey,
				AppName:        srv.Spec().Name,
				Channel:        opts.UpdateChannel,
				InstallationID: srv.Identity().ServerID,
				TrustedKeys:    trusted,
				BaseDir:        baseDir,
				HealthURL:      localURL + "/v1/health",
				CurrentTargetVersion: func(target ckupdater.UpdateTarget) (string, string) {
					switch target {
					case ckupdater.TargetWin64:
						if loadedManifest != nil && loadedManifest.AppVersion != "" {
							return loadedManifest.AppVersion, ""
						}
						return srv.Spec().Version, ""
					case ckupdater.TargetGame:
						if loadedManifest != nil {
							return loadedManifest.Game.Version, loadedManifest.Game.SHA256
						}
					case ckupdater.TargetCompanionWeb:
						if loadedManifest != nil && loadedManifest.AppVersion != "" {
							return loadedManifest.AppVersion, ""
						}
					}
					return "", ""
				},
				IsIdle:      func() bool { return srv.Hub().Snapshot().Phase == ckframework.PhaseIdle },
				StopGame:    func() { sup.Stop() },
				RestartGame: func() { sup.Restart() },
				OnExitForSelfUpdate: func() {
					stop()
					_ = srv.Close(context.Background())
				},
				OnStatus: func(st ckupdater.Status) {
					if st.State == "staged" || st.State == "applying" || st.State == "error" {
						sev := "info"
						if st.State == "error" {
							sev = "warn"
						}
						_, _ = srv.Outbox().Record(context.Background(), cktelemetry.Event{
							Category: "launcher",
							Kind:     "updater." + st.State,
							Severity: sev,
							Payload: map[string]any{
								"channel":         st.Channel,
								"pending_target":  st.PendingTarget,
								"pending_version": st.PendingVersion,
								"policy":          st.Policy,
								"error":           st.LastError,
							},
						})
					}
				},
			})
			_ = upd.ApplyPendingOnBoot()
			go upd.Run(ctx)
		}
	}

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

	// Native Fyne v2 event loop runs on the main OS thread.
	err = RunNativeUI(ctx, srv, cd, sup, upd, port)
	stop()
	sup.Stop()
	return err
}

// OpenBrowser opens url in the system's default web browser without flashing a console window.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		setHideWindow(cmd)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func exeBaseDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	dir := filepath.Dir(exe)
	if filepath.Base(dir) == "bin" {
		return filepath.Dir(dir)
	}
	return dir
}

func resolveRelative(base, target string) string {
	if target == "" || filepath.IsAbs(target) {
		return target
	}
	return filepath.Clean(filepath.Join(base, filepath.FromSlash(target)))
}

func resolveExistingPath(baseDir, candidate string) string {
	if candidate == "" {
		return ""
	}
	if filepath.IsAbs(candidate) {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return ""
	}
	fromBase := filepath.Join(baseDir, filepath.FromSlash(candidate))
	if _, err := os.Stat(fromBase); err == nil {
		return fromBase
	}
	if _, err := os.Stat(candidate); err == nil {
		abs, _ := filepath.Abs(candidate)
		return abs
	}
	return ""
}

func discoverCompanionDir(baseDir string) string {
	candidates := []string{
		filepath.Join(baseDir, "companion"),
		filepath.Join(baseDir, "..", "companion-rn", "dist"),
		"companion",
		filepath.Join("..", "companion-rn", "dist"),
	}
	for _, c := range candidates {
		idx := filepath.Join(c, "index.html")
		if info, err := os.Stat(idx); err == nil && !info.IsDir() {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}
