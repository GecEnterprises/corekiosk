package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckpack"
)

type devOptions struct {
	configPath    string
	rootFlag      string
	listenAddr    string
	headless      bool
	noCompanion   bool
	noGame        bool
	noOpen        bool
	noWatch       bool
	godotPath     string
	webPort       string
	extraGameArgs []string
}

func runDev(args []string) error {
	var passthroughGameArgs []string
	for i, a := range args {
		if a == "--" {
			passthroughGameArgs = args[i+1:]
			args = args[:i]
			break
		}
	}

	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	opts := devOptions{extraGameArgs: passthroughGameArgs}
	fs.StringVar(&opts.configPath, "config", "kiosk.json", "Path to kiosk.json")
	fs.StringVar(&opts.rootFlag, "root", "", "Monorepo root directory")
	fs.StringVar(&opts.listenAddr, "listen", "", "Override server listen address (default :8080)")
	fs.BoolVar(&opts.headless, "headless", false, "Run server without opening the native Fyne window")
	fs.BoolVar(&opts.noCompanion, "no-companion", false, "Do not start Expo web dev server")
	fs.BoolVar(&opts.noGame, "no-game", false, "Do not launch game")
	fs.BoolVar(&opts.noOpen, "no-open", false, "Do not auto-open Companion Web in browser")
	fs.BoolVar(&opts.noWatch, "no-watch", false, "Disable Go source file watcher")
	fs.StringVar(&opts.godotPath, "godot", "", "Explicit path to Godot executable")
	fs.StringVar(&opts.webPort, "web-port", "8081", "Expo web dev server port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		opts.extraGameArgs = append(opts.extraGameArgs, fs.Args()...)
	}

	rootDir, cfg, err := ckpack.LoadOrDetectConfig(opts.configPath, opts.rootFlag)
	if err != nil {
		return err
	}

	listenAddr := opts.listenAddr
	if listenAddr == "" {
		listenAddr = cfg.Server.Listen
	}
	if listenAddr == "" {
		listenAddr = ":8080"
	}
	_, apiPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		if !strings.Contains(listenAddr, ":") {
			apiPort = listenAddr
			listenAddr = ":" + apiPort
		} else {
			return fmt.Errorf("invalid -listen %q: %w", listenAddr, err)
		}
	}

	serverDir := ckpack.ResolvePath(rootDir, cfg.Server.Dir)
	if !ckpack.FileExists(filepath.Join(serverDir, "go.mod")) {
		return fmt.Errorf("server go.mod not found at %s", serverDir)
	}
	compDir := ckpack.ResolvePath(rootDir, cfg.Companion.Dir)
	gameDir := ckpack.ResolvePath(rootDir, cfg.Game.Dir)

	tokBytes := make([]byte, 12)
	_, _ = rand.Read(tokBytes)
	bridgeToken := "dev-" + hex.EncodeToString(tokBytes)
	serverURL := "http://127.0.0.1:" + apiPort

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := &prefixLogger{}
	out.log("dev", "%s v%s (root: %s)", cfg.Name, cfg.Version, rootDir)
	out.log("dev", "shortcuts: [r]+Enter restart game | [s]+Enter rebuild server | [o]+Enter open web | [q]+Enter quit")

	pm := newProcGroup(out)
	defer pm.killAll()

	// Temp dev server binary path (reused across live reloads for fast incremental Go linking).
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	tmpBin := filepath.Join(os.TempDir(), fmt.Sprintf("corekiosk-dev-%s-%d%s", sanitizeName(cfg.Name), os.Getpid(), ext))
	defer os.Remove(tmpBin)

	serverMu := sync.Mutex{}
	var serverCmd *exec.Cmd

	buildAndStartServer := func(reason string) error {
		serverMu.Lock()
		defer serverMu.Unlock()

		out.log("server", "building (%s)...", reason)
		buildCmd := exec.CommandContext(ctx, "go", "build", "-o", tmpBin, ".")
		buildCmd.Dir = serverDir
		buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		if raw, err := buildCmd.CombinedOutput(); err != nil {
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line != "" {
					out.log("build!", "%s", line)
				}
			}
			return err
		}

		if serverCmd != nil {
			pm.killOne(serverCmd)
			serverCmd = nil
			time.Sleep(150 * time.Millisecond)
		}

		srvArgs := []string{"-listen", listenAddr, "-no-game", "-no-companion"}
		if opts.headless {
			srvArgs = append(srvArgs, "-headless")
		}
		envExtra := []string{
			"CK_DEV=1",
			"CK_BRIDGE_TOKEN=" + bridgeToken,
		}
		cmd, err := pm.start(ctx, "server", serverDir, envExtra, tmpBin, srvArgs...)
		if err != nil {
			return err
		}
		serverCmd = cmd
		return nil
	}

	if err := buildAndStartServer("initial"); err != nil {
		return fmt.Errorf("initial server build failed: %w", err)
	}

	// Wait briefly for server /v1/health before starting clients.
	waitHTTPReady(ctx, serverURL+"/v1/health", 6*time.Second)

	// Start Go file watcher for ./server
	if !opts.noWatch {
		go watchGoFiles(ctx, serverDir, func(changedFile string) {
			_ = buildAndStartServer(filepath.Base(changedFile) + " changed")
		})
	}

	// Start Expo React Native Web dev server (`./companion-rn`)
	webURL := "http://localhost:" + opts.webPort
	if !opts.noCompanion && cfg.Companion.Dir != "" && ckpack.DirExists(compDir) {
		if !ckpack.DirExists(filepath.Join(compDir, "node_modules")) {
			out.log("companion", "installing node_modules...")
			_ = runLogged(ctx, out, "companion", compDir, ckpack.NpmBin(), "install")
		}
		_, err := pm.start(ctx, "companion", compDir, []string{"BROWSER=none"}, ckpack.NpxBin(), "expo", "start", "--web", "--port", opts.webPort)
		if err != nil {
			out.log("companion", "failed to start expo: %v", err)
		} else if !opts.noOpen {
			go func() {
				if waitHTTPReady(ctx, "http://127.0.0.1:"+opts.webPort, 30*time.Second) {
					out.log("companion", "opening %s", webURL)
					_ = openURL(webURL)
				}
			}()
		}
	}

	// Start Godot 4 directly against `./game`
	gameMu := sync.Mutex{}
	var gameCmd *exec.Cmd
	startGame := func() {
		if opts.noGame || cfg.Game.Dir == "" || !ckpack.FileExists(filepath.Join(gameDir, "project.godot")) {
			return
		}
		gameMu.Lock()
		defer gameMu.Unlock()

		if gameCmd != nil {
			pm.killOne(gameCmd)
			gameCmd = nil
			time.Sleep(150 * time.Millisecond)
		}
		godotExe, godotVer, err := ckpack.ResolveGodot(opts.godotPath)
		if err != nil {
			out.log("game", "%v", err)
			return
		}
		gArgs := []string{
			"--path", gameDir,
			"--",
			"--windowed",
			"--server-url=" + serverURL,
			"--bridge-token=" + bridgeToken,
		}
		gArgs = append(gArgs, cfg.Game.DevArgs...)
		gArgs = append(gArgs, opts.extraGameArgs...)
		out.log("game", "launching Godot %s", godotVer)
		cmd, err := pm.start(ctx, "game", gameDir, nil, godotExe, gArgs...)
		if err != nil {
			out.log("game", "failed to launch godot: %v", err)
			return
		}
		gameCmd = cmd
	}
	startGame()

	// Interactive stdin command reader (`r`, `s`, `o`, `q`)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			switch strings.ToLower(strings.TrimSpace(sc.Text())) {
			case "r":
				startGame()
			case "s":
				_ = buildAndStartServer("manual reload")
			case "o":
				out.log("dev", "opening %s", webURL)
				_ = openURL(webURL)
			case "q", "quit", "exit":
				stop()
				return
			}
		}
	}()

	<-ctx.Done()
	out.log("dev", "shutting down all dev processes...")
	return nil
}

type prefixLogger struct {
	mu sync.Mutex
}

func (p *prefixLogger) log(tag, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	p.mu.Lock()
	fmt.Printf("[%-9s] %s\n", tag, msg)
	p.mu.Unlock()
}

func (p *prefixLogger) pipe(tag string, r io.Reader) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		p.log(tag, "%s", line)
	}
}

type procGroup struct {
	mu   sync.Mutex
	cmds map[*exec.Cmd]string
	out  *prefixLogger
}

func newProcGroup(out *prefixLogger) *procGroup {
	return &procGroup{cmds: make(map[*exec.Cmd]string), out: out}
}

func (pg *procGroup) start(_ context.Context, tag, dir string, extraEnv []string, bin string, args ...string) (*exec.Cmd, error) {
	// Do NOT use exec.CommandContext directly because Go's default Kill on Windows only kills
	// the top wrapper process (e.g. cmd.exe / npx.cmd), leaving Node/Metro children orphaned.
	// Instead we kill full process trees via taskkill /T /F in killOne / killAll.
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	pg.mu.Lock()
	pg.cmds[cmd] = tag
	pg.mu.Unlock()

	go pg.out.pipe(tag, stdout)
	go pg.out.pipe(tag, stderr)
	go func() {
		err := cmd.Wait()
		pg.mu.Lock()
		_, tracked := pg.cmds[cmd]
		delete(pg.cmds, cmd)
		pg.mu.Unlock()
		if tracked && err != nil {
			pg.out.log(tag, "exited (%v)", err)
		}
	}()
	return cmd, nil
}

func (pg *procGroup) killOne(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pg.mu.Lock()
	delete(pg.cmds, cmd)
	pg.mu.Unlock()
	killProcessTree(cmd.Process.Pid)
}

func (pg *procGroup) killAll() {
	pg.mu.Lock()
	list := make([]*exec.Cmd, 0, len(pg.cmds))
	for c := range pg.cmds {
		list = append(list, c)
	}
	pg.cmds = make(map[*exec.Cmd]string)
	pg.mu.Unlock()

	for _, c := range list {
		if c.Process != nil {
			killProcessTree(c.Process.Pid)
		}
	}
}

func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid)).Run()
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func runLogged(ctx context.Context, out *prefixLogger, tag, dir, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	go out.pipe(tag, stdout)
	go out.pipe(tag, stderr)
	return cmd.Wait()
}

func waitHTTPReady(ctx context.Context, url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 400 * time.Millisecond}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func watchGoFiles(ctx context.Context, dir string, onChange func(changed string)) {
	snapshot := func() map[string]int64 {
		m := make(map[string]int64)
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				base := info.Name()
				if base == "bin" || base == "data" || base == "node_modules" || strings.HasPrefix(base, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			name := info.Name()
			if strings.HasSuffix(name, ".go") || name == "go.mod" || name == "go.sum" {
				m[path] = info.ModTime().UnixNano()
			}
			return nil
		})
		return m
	}

	prev := snapshot()
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := snapshot()
			changed := ""
			if len(cur) != len(prev) {
				changed = "files"
			} else {
				for k, v := range cur {
					if prev[k] != v {
						changed = k
						break
					}
				}
			}
			if changed != "" {
				// Debounce rapid multi-file saves
				time.Sleep(250 * time.Millisecond)
				prev = snapshot()
				onChange(changed)
			}
		}
	}
}

func openURL(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, s)
}
