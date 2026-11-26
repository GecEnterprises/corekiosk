package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cklauncher"
)

// KioskProjectConfig defines the declarative activation monorepo build spec (`kiosk.json`).
type KioskProjectConfig struct {
	Name             string               `json:"name"`
	Version          string               `json:"version"`
	MinClientVersion string               `json:"min_client_version,omitempty"`
	Server           ServerBuildConfig    `json:"server"`
	Game             GameBuildConfig      `json:"game"`
	Companion        CompanionBuildConfig `json:"companion"`
}

type ServerBuildConfig struct {
	Dir        string `json:"dir"`
	Exe        string `json:"exe"`
	WindowsGUI *bool  `json:"windows_gui,omitempty"`
}

type GameBuildConfig struct {
	Dir          string   `json:"dir"`
	Preset       string   `json:"preset"`
	Exe          string   `json:"exe"`
	Version      string   `json:"version,omitempty"`
	RequiredDLLs []string `json:"required_dlls,omitempty"`
}

type CompanionBuildConfig struct {
	Dir  string `json:"dir"`
	Port int    `json:"port,omitempty"`
}

func main() {
	// Support both `ckpack build [flags]` and `ckpack [flags]`.
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "build" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("ckpack", flag.ExitOnError)
	cfgPath := fs.String("config", "kiosk.json", "Path to activation monorepo kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory (defaults to directory of -config or cwd)")
	outDir := fs.String("out", "./dist", "Output directory for staged folder and release zip")
	debugMode := fs.Bool("debug", false, "Build Debug configuration instead of Release")
	skipTests := fs.Bool("skip-tests", false, "Skip Go unit tests and TypeScript typecheck")
	skipCompanion := fs.Bool("skip-companion", false, "Skip building Companion React Native Web bundle")
	skipGame := fs.Bool("skip-game", false, "Skip exporting Godot game")
	skipImport := fs.Bool("skip-import", false, "Skip headless Godot resource --import step")
	godotFlag := fs.String("godot", "", "Explicit path to Godot 4 executable")
	buildAndroid := fs.Bool("android", false, "Also build Android release APK from companion-rn/android")
	noZip := fs.Bool("no-zip", false, "Stage release directory without creating a .zip archive")

	// Legacy stage-only flags (when -launcher is passed explicitly)
	appName := fs.String("app", "", "Activation app name (legacy stage mode or override)")
	appVer := fs.String("app-version", "", "Activation SemVer override")
	minClient := fs.String("min-client-version", "", "Minimum companion client SemVer override")
	launcherExe := fs.String("launcher", "", "Pre-built launcher binary path (legacy stage-only mode)")
	gameDir := fs.String("game-dir", "", "Pre-exported game directory (legacy stage-only mode)")
	gameBin := fs.String("game-bin", "game.exe", "Game binary name inside game/ (legacy stage-only mode)")
	gameVer := fs.String("game-version", "0.1.0", "Game SemVer (legacy stage-only mode)")
	gameEngine := fs.String("engine", "godot", "Game engine identifier (legacy stage-only mode)")

	_ = fs.Parse(args)

	if *launcherExe != "" {
		runLegacyStage(*appName, *appVer, *minClient, *launcherExe, *gameDir, *gameBin, *gameVer, *gameEngine, *outDir)
		return
	}

	if err := runMonorepoBuild(monorepoOpts{
		configPath:    *cfgPath,
		rootOverride:  *rootFlag,
		outDir:        *outDir,
		appOverride:   *appName,
		verOverride:   *appVer,
		minClient:     *minClient,
		debug:         *debugMode,
		skipTests:     *skipTests,
		skipCompanion: *skipCompanion,
		skipGame:      *skipGame,
		skipImport:    *skipImport,
		godotPath:     *godotFlag,
		buildAndroid:  *buildAndroid,
		noZip:         *noZip,
	}); err != nil {
		fatal(err)
	}
}

type monorepoOpts struct {
	configPath    string
	rootOverride  string
	outDir        string
	appOverride   string
	verOverride   string
	minClient     string
	debug         bool
	skipTests     bool
	skipCompanion bool
	skipGame      bool
	skipImport    bool
	godotPath     string
	buildAndroid  bool
	noZip         bool
}

func runMonorepoBuild(opts monorepoOpts) error {
	rootDir, cfg, err := loadOrDetectConfig(opts)
	if err != nil {
		return err
	}

	outBase := resolvePath(rootDir, opts.outDir)
	stageDir := filepath.Join(outBase, cfg.Name)
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("clean stage dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create stage dir: %w", err)
	}

	gitSha := gitShortRev(rootDir)
	modeName := "Release"
	if opts.debug {
		modeName = "Debug"
	}

	step("Packaging %s v%s (%s)", cfg.Name, cfg.Version, modeName)
	fmt.Printf("  Root   : %s\n", rootDir)
	fmt.Printf("  Stage  : %s\n", stageDir)
	if gitSha != "" {
		fmt.Printf("  Commit : %s\n", gitSha)
	}

	// 1. Companion React Native Web (+ optional Android APK)
	var compManifest cklauncher.CompanionManifest
	compSrcDir := resolvePath(rootDir, cfg.Companion.Dir)
	if !opts.skipCompanion && cfg.Companion.Dir != "" && dirExists(compSrcDir) {
		step("1/4 Building Companion Web (%s)", cfg.Companion.Dir)
		if !dirExists(filepath.Join(compSrcDir, "node_modules")) {
			if err := runCmd(compSrcDir, nil, npmBin(), "install"); err != nil {
				return fmt.Errorf("companion npm install: %w", err)
			}
		}
		if !opts.skipTests && hasScript(compSrcDir, "typecheck") {
			if err := runCmd(compSrcDir, nil, npmBin(), "run", "typecheck"); err != nil {
				return fmt.Errorf("companion typecheck failed: %w", err)
			}
		}
		if err := runCmd(compSrcDir, nil, npxBin(), "expo", "export", "--platform", "web"); err != nil {
			return fmt.Errorf("expo web export failed: %w", err)
		}
		compStage := filepath.Join(stageDir, "companion")
		if err := copyDir(filepath.Join(compSrcDir, "dist"), compStage); err != nil {
			return fmt.Errorf("copy companion web dist: %w", err)
		}
		port := cfg.Companion.Port
		if port <= 0 {
			port = 8090
		}
		compManifest = cklauncher.CompanionManifest{
			Dir:  "./companion",
			Port: port,
		}

		if opts.buildAndroid {
			step("1b/4 Building Companion Android APK")
			apkName, err := buildCompanionAndroid(compSrcDir, stageDir, cfg.Name)
			if err != nil {
				return err
			}
			compManifest.APK = "./" + apkName
		}
	} else {
		step("1/4 Companion build skipped")
	}

	// 2. Godot 4 Game Export
	var gameManifest cklauncher.GameManifest
	gameSrcDir := resolvePath(rootDir, cfg.Game.Dir)
	if !opts.skipGame && cfg.Game.Dir != "" && fileExists(filepath.Join(gameSrcDir, "project.godot")) {
		step("2/4 Exporting Godot Game (%s / %s)", cfg.Game.Dir, cfg.Game.Preset)
		godotExe, godotVer, err := resolveGodot(opts.godotPath)
		if err != nil {
			return err
		}
		fmt.Printf("  Godot  : %s (%s)\n", godotVer, godotExe)

		if err := verifyGodotTemplates(gameSrcDir, cfg.Game.Preset, godotVer); err != nil {
			return err
		}

		if !opts.skipImport {
			if err := runCmd(gameSrcDir, nil, godotExe, "--headless", "--path", gameSrcDir, "--import"); err != nil {
				return fmt.Errorf("godot --import failed: %w", err)
			}
		}

		gameStageDir := filepath.Join(stageDir, "game")
		if err := os.MkdirAll(gameStageDir, 0o755); err != nil {
			return err
		}
		gameOutExe := filepath.Join(gameStageDir, cfg.Game.Exe)
		exportFlag := "--export-release"
		if opts.debug {
			exportFlag = "--export-debug"
		}
		if err := runCmd(gameSrcDir, nil, godotExe, "--headless", "--path", gameSrcDir, exportFlag, cfg.Game.Preset, gameOutExe); err != nil {
			return fmt.Errorf("godot %s failed: %w", exportFlag, err)
		}
		if !fileExists(gameOutExe) {
			return fmt.Errorf("godot export did not produce %s", gameOutExe)
		}

		// Ensure required GDExtension / runtime DLLs are present in stage/game.
		for _, relDLL := range cfg.Game.RequiredDLLs {
			adjusted := relDLL
			if opts.debug {
				adjusted = strings.ReplaceAll(adjusted, "template_release", "template_debug")
			}
			srcDLL := filepath.Join(gameSrcDir, filepath.FromSlash(adjusted))
			baseName := filepath.Base(srcDLL)
			if !fileExistsRecursive(gameStageDir, baseName) {
				if !fileExists(srcDLL) {
					return fmt.Errorf("missing required GDExtension DLL: %s", srcDLL)
				}
				if err := copyFile(srcDLL, filepath.Join(gameStageDir, baseName)); err != nil {
					return err
				}
			}
		}

		exeHash, _ := cklauncher.HashFile(gameOutExe)
		pckPath := strings.TrimSuffix(gameOutExe, filepath.Ext(gameOutExe)) + ".pck"
		pckHash, _ := cklauncher.HashFile(pckPath)

		gVer := cfg.Game.Version
		if gVer == "" {
			gVer = cfg.Version
		}
		gameManifest = cklauncher.GameManifest{
			Binary:  "./game/" + filepath.ToSlash(cfg.Game.Exe),
			Version: gVer,
			Engine:  "godot-" + godotVer,
			SHA256:  exeHash,
			PCKHash: pckHash,
		}
	} else {
		step("2/4 Godot game export skipped")
	}

	// 3. Go ckserver + Fyne cklauncher executable
	serverSrcDir := resolvePath(rootDir, cfg.Server.Dir)
	if !fileExists(filepath.Join(serverSrcDir, "go.mod")) {
		return fmt.Errorf("server go.mod not found in %s", serverSrcDir)
	}
	step("3/4 Building Go Server & Fyne Launcher (%s)", cfg.Server.Dir)
	if !opts.skipTests {
		if err := runCmd(serverSrcDir, nil, "go", "test", "./..."); err != nil {
			return fmt.Errorf("server unit tests failed: %w", err)
		}
	}

	launcherOut := filepath.Join(stageDir, cfg.Server.Exe)
	winGUI := true
	if cfg.Server.WindowsGUI != nil {
		winGUI = *cfg.Server.WindowsGUI
	}
	var ldflags []string
	if !opts.debug {
		ldflags = append(ldflags, "-s", "-w")
	}
	if runtime.GOOS == "windows" && winGUI && !opts.debug {
		ldflags = append(ldflags, "-H=windowsgui")
	} else if runtime.GOOS == "windows" && winGUI {
		ldflags = append(ldflags, "-H=windowsgui")
	}

	goArgs := []string{"build", "-buildvcs=false"}
	if !opts.debug {
		goArgs = append(goArgs, "-trimpath")
	}
	if len(ldflags) > 0 {
		goArgs = append(goArgs, "-ldflags", strings.Join(ldflags, " "))
	}
	goArgs = append(goArgs, "-o", launcherOut, ".")

	// Fyne v2 requires CGO_ENABLED=1.
	envOverrides := []string{"CGO_ENABLED=1"}
	if err := runCmd(serverSrcDir, envOverrides, "go", goArgs...); err != nil {
		return fmt.Errorf("go build failed: %w", err)
	}

	// 4. Write kiosk.manifest.json, README.txt, and release .zip
	step("4/4 Writing kiosk.manifest.json and release bundle")
	manifest := cklauncher.Manifest{
		AppName:            cfg.Name,
		AppVersion:         cfg.Version,
		CKFrameworkVersion: ckframework.Version,
		CKLauncherVersion:  cklauncher.FullVersion(),
		ProtocolVersion:    ckframework.ProtocolVersion,
		MinClientVersion:   cfg.MinClientVersion,
		Game:               gameManifest,
		Companion:          compManifest,
		GitCommit:          gitSha,
		BuiltAt:            time.Now().UTC().Format(time.RFC3339),
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "kiosk.manifest.json"), manifestBytes, 0o644); err != nil {
		return err
	}
	if err := writeQuickStartReadme(stageDir, cfg, compManifest); err != nil {
		return err
	}

	if opts.noZip {
		fmt.Printf("\nStaged release ready at: %s\n", stageDir)
		return nil
	}

	archTag := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	zipName := fmt.Sprintf("%s-v%s-%s.zip", cfg.Name, cfg.Version, archTag)
	zipPath := filepath.Join(outBase, zipName)
	if err := createZip(stageDir, zipPath, cfg.Name); err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	zipHash, _ := cklauncher.HashFile(zipPath)
	zipStat, _ := os.Stat(zipPath)

	step("Release Package Complete")
	fmt.Printf("  Folder : %s\n", stageDir)
	fmt.Printf("  Zip    : %s (%.2f MB)\n", zipPath, float64(zipStat.Size())/(1024*1024))
	fmt.Printf("  SHA256 : %s\n", zipHash)
	return nil
}

func loadOrDetectConfig(opts monorepoOpts) (string, KioskProjectConfig, error) {
	var cfg KioskProjectConfig
	rootDir := opts.rootOverride
	if rootDir == "" {
		rootDir = "."
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", cfg, err
	}

	cfgFile := opts.configPath
	if !filepath.IsAbs(cfgFile) {
		cfgFile = filepath.Join(absRoot, cfgFile)
	}
	if raw, err := os.ReadFile(cfgFile); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", cfg, fmt.Errorf("parse %s: %w", cfgFile, err)
		}
		if opts.rootOverride == "" {
			absRoot = filepath.Dir(cfgFile)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", cfg, err
	}

	// Apply conventional corekiosk monorepo defaults for any omitted fields.
	if opts.appOverride != "" {
		cfg.Name = opts.appOverride
	}
	if cfg.Name == "" {
		cfg.Name = strings.ToLower(filepath.Base(absRoot))
	}
	if opts.verOverride != "" {
		cfg.Version = opts.verOverride
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if opts.minClient != "" {
		cfg.MinClientVersion = opts.minClient
	}
	if cfg.Server.Dir == "" {
		cfg.Server.Dir = "./server"
	}
	if cfg.Server.Exe == "" {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		cfg.Server.Exe = cfg.Name + ext
	}
	if cfg.Game.Dir == "" && dirExists(filepath.Join(absRoot, "game")) {
		cfg.Game.Dir = "./game"
	}
	if cfg.Game.Preset == "" {
		cfg.Game.Preset = "Windows Desktop"
	}
	if cfg.Game.Exe == "" {
		cfg.Game.Exe = cfg.Name + "-game.exe"
	}
	if cfg.Companion.Dir == "" && dirExists(filepath.Join(absRoot, "companion-rn")) {
		cfg.Companion.Dir = "./companion-rn"
	}
	if cfg.Companion.Port <= 0 {
		cfg.Companion.Port = 8090
	}
	return absRoot, cfg, nil
}

func buildCompanionAndroid(compSrcDir, stageDir, appName string) (string, error) {
	androidDir := filepath.Join(compSrcDir, "android")
	if !dirExists(androidDir) {
		if err := runCmd(compSrcDir, nil, npxBin(), "expo", "prebuild", "--platform", "android"); err != nil {
			return "", fmt.Errorf("expo prebuild android: %w", err)
		}
	}
	gradleWrapper := "gradlew"
	if runtime.GOOS == "windows" {
		gradleWrapper = "gradlew.bat"
	}
	if err := runCmd(androidDir, nil, filepath.Join(androidDir, gradleWrapper), "assembleRelease"); err != nil {
		return "", fmt.Errorf("gradle assembleRelease: %w", err)
	}
	apkSrc := filepath.Join(androidDir, "app", "build", "outputs", "apk", "release", "app-release.apk")
	if !fileExists(apkSrc) {
		return "", fmt.Errorf("expected release APK not found at %s", apkSrc)
	}
	apkName := appName + "-companion.apk"
	if err := copyFile(apkSrc, filepath.Join(stageDir, apkName)); err != nil {
		return "", err
	}
	return apkName, nil
}

func resolveGodot(preferred string) (string, string, error) {
	candidates := []string{"godot_console", "godot4_console", "godot", "godot4"}
	var resolved string
	if preferred != "" {
		if fileExists(preferred) {
			resolved, _ = filepath.Abs(preferred)
		} else if p, err := exec.LookPath(preferred); err == nil {
			resolved = p
		} else {
			return "", "", fmt.Errorf("godot executable not found at %q", preferred)
		}
	} else {
		for _, c := range candidates {
			if p, err := exec.LookPath(c); err == nil {
				resolved = p
				break
			}
		}
	}
	if resolved == "" {
		return "", "", fmt.Errorf("godot executable not found on PATH (tried %s); pass -godot <path> or -skip-game", strings.Join(candidates, ", "))
	}

	if realPath, err := filepath.EvalSymlinks(resolved); err == nil && realPath != "" {
		resolved = realPath
	}
	// On Windows, prefer the synchronous *_console.exe wrapper if available.
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(resolved), "_console.exe") && strings.HasSuffix(strings.ToLower(resolved), ".exe") {
		consoleSibling := resolved[:len(resolved)-4] + "_console.exe"
		if fileExists(consoleSibling) {
			resolved = consoleSibling
		}
	}

	out, err := exec.Command(resolved, "--version").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("failed to query godot --version (%s): %w", resolved, err)
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	re := regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?\.[a-zA-Z0-9_]+(?:\.mono)?)`)
	m := re.FindStringSubmatch(line)
	if len(m) < 2 {
		return resolved, line, nil
	}
	return resolved, m[1], nil
}

func verifyGodotTemplates(gameDir, preset, godotTemplateVer string) error {
	presetsPath := filepath.Join(gameDir, "export_presets.cfg")
	if !fileExists(presetsPath) {
		return fmt.Errorf("missing %s", presetsPath)
	}
	raw, err := os.ReadFile(presetsPath)
	if err != nil {
		return err
	}
	if !strings.Contains(string(raw), `name="`+preset+`"`) {
		return fmt.Errorf("export preset %q not found in %s", preset, presetsPath)
	}

	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData != "" && godotTemplateVer != "" {
			tplDir := filepath.Join(appData, "Godot", "export_templates", godotTemplateVer)
			relTpl := filepath.Join(tplDir, "windows_release_x86_64.exe")
			if !fileExists(relTpl) {
				return fmt.Errorf("Godot export template not found at %s (install via Godot Editor -> Manage Export Templates, or pass -skip-game)", relTpl)
			}
		}
	}
	return nil
}

func writeQuickStartReadme(stageDir string, cfg KioskProjectConfig, comp cklauncher.CompanionManifest) error {
	lines := []string{
		fmt.Sprintf("%s v%s (corekiosk %s)", cfg.Name, cfg.Version, ckframework.Version),
		"================================================================",
		"",
		"Quick Start:",
		fmt.Sprintf("  1. Extract this folder to a writable path (e.g. C:\\%s).", cfg.Name),
		fmt.Sprintf("  2. Run %s.", cfg.Server.Exe),
		"     - Starts the corekiosk server on all interfaces (0.0.0.0:8080).",
	}
	if comp.Dir != "" {
		lines = append(lines, fmt.Sprintf("     - Hosts the Companion Web app on port %d (click 'Companion' in the launcher UI or open http://<kiosk-ip>:%d on any tablet/browser).", comp.Port, comp.Port))
	}
	if cfg.Game.Dir != "" {
		lines = append(lines, fmt.Sprintf("     - Launches and supervises ./game/%s after the pre-launch countdown.", cfg.Game.Exe))
	}
	lines = append(lines,
		"     - Default operator login: operator / operator (update in the Operator panel).",
		"",
		"CLI Flags:",
		fmt.Sprintf("  .\\%s -now               # Launch game immediately (skip countdown)", cfg.Server.Exe),
		fmt.Sprintf("  .\\%s -open-companion    # Automatically open Companion Web in browser", cfg.Server.Exe),
		fmt.Sprintf("  .\\%s -no-game           # Run server + launcher UI without spawning game", cfg.Server.Exe),
		fmt.Sprintf("  .\\%s -install-autostart # Register Windows logon Task Scheduler autostart", cfg.Server.Exe),
		"",
	)
	return os.WriteFile(filepath.Join(stageDir, "README.txt"), []byte(strings.Join(lines, "\r\n")), 0o644)
}

func createZip(sourceDir, zipPath, rootPrefix string) error {
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		entryName := filepath.ToSlash(rel)
		if rootPrefix != "" {
			entryName = rootPrefix + "/" + entryName
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = entryName
		hdr.Method = zip.Deflate

		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
}

func runLegacyStage(appName, appVer, minClient, launcherExe, gameDir, gameBin, gameVer, gameEngine, outDir string) {
	if appName == "" {
		fmt.Fprintln(os.Stderr, "usage: ckpack build [-config kiosk.json] OR ckpack -app <name> -launcher <exe>")
		os.Exit(2)
	}
	if appVer == "" {
		appVer = "0.1.0"
	}
	dest := filepath.Join(outDir, appName)
	if err := os.MkdirAll(filepath.Join(dest, "game"), 0o755); err != nil {
		fatal(err)
	}
	launcherDst := filepath.Join(dest, appName+"-launcher"+filepath.Ext(launcherExe))
	if err := copyFile(launcherExe, launcherDst); err != nil {
		fatal(err)
	}
	gameHash := ""
	if gameDir != "" {
		if err := copyDir(gameDir, filepath.Join(dest, "game")); err != nil {
			fatal(err)
		}
		binPath := filepath.Join(dest, "game", gameBin)
		if h, err := cklauncher.HashFile(binPath); err == nil {
			gameHash = h
		}
	}
	manifest := cklauncher.Manifest{
		AppName:            appName,
		AppVersion:         appVer,
		CKFrameworkVersion: ckframework.Version,
		CKLauncherVersion:  cklauncher.FullVersion(),
		ProtocolVersion:    ckframework.ProtocolVersion,
		MinClientVersion:   minClient,
		Game: cklauncher.GameManifest{
			Binary:  "./game/" + filepath.ToSlash(gameBin),
			Version: gameVer,
			Engine:  gameEngine,
			SHA256:  gameHash,
		},
		BuiltAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "kiosk.manifest.json"), raw, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("Packaged %s -> %s\n", appName, dest)
}

func runCmd(dir string, extraEnv []string, bin string, args ...string) error {
	fmt.Printf("  $ %s %s\n", filepath.Base(bin), strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd.Run()
}

func npmBin() string {
	if runtime.GOOS == "windows" {
		return "npm.cmd"
	}
	return "npm"
}

func npxBin() string {
	if runtime.GOOS == "windows" {
		return "npx.cmd"
	}
	return "npx"
}

func hasScript(pkgDir, scriptName string) bool {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return false
	}
	var parsed struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return false
	}
	_, ok := parsed.Scripts[scriptName]
	return ok
}

func gitShortRev(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--short=8", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func resolvePath(root, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExistsRecursive(dir, filename string) bool {
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.EqualFold(info.Name(), filename) {
			found = true
			return io.EOF
		}
		return nil
	})
	return found
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func step(format string, args ...any) {
	fmt.Printf("\n==> "+format+"\n", args...)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "\nckpack error: %v\n", err)
	os.Exit(1)
}
