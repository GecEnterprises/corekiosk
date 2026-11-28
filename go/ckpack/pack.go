package ckpack

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/GecEnterprises/corekiosk/go/ckdiscovery"
	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

// KioskProjectConfig defines the declarative activation monorepo build spec (`kiosk.json`).
type KioskProjectConfig struct {
	Name             string                     `json:"name"`
	Version          string                     `json:"version"`
	MinClientVersion string                     `json:"min_client_version,omitempty"`
	Server           ServerBuildConfig          `json:"server"`
	Game             GameBuildConfig            `json:"game"`
	Companion        CompanionBuildConfig       `json:"companion"`
	Updates          UpdatesBuildConfig         `json:"updates,omitempty"`
	Telemetry        TelemetryBuildConfig       `json:"telemetry,omitempty"`
	Discovery        ckdiscovery.ManifestConfig `json:"discovery,omitempty"`
}

type UpdatesBuildConfig struct {
	URL         string `json:"url,omitempty"`
	Channel     string `json:"channel,omitempty"`
	TrustedKeys string `json:"trusted_keys,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	SupabaseURL string `json:"supabase_url,omitempty"`
}

type TelemetryBuildConfig struct {
	URL string `json:"url,omitempty"`
}

type ServerBuildConfig struct {
	Dir        string `json:"dir"`
	Exe        string `json:"exe"`
	Listen     string `json:"listen,omitempty"`
	WindowsGUI *bool  `json:"windows_gui,omitempty"`
}

type GameBuildConfig struct {
	Dir          string   `json:"dir"`
	Preset       string   `json:"preset"`
	Exe          string   `json:"exe"`
	Version      string   `json:"version,omitempty"`
	DevArgs      []string `json:"dev_args,omitempty"`
	RequiredDLLs []string `json:"required_dlls,omitempty"`
}

type CompanionBuildConfig struct {
	Dir  string `json:"dir"`
	Port int    `json:"port,omitempty"`
}

type GameManifest struct {
	Binary  string `json:"binary"`
	Version string `json:"version"`
	Engine  string `json:"engine,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	PCKHash string `json:"pck_sha256,omitempty"`
}

type CompanionManifest struct {
	Dir  string `json:"dir,omitempty"`
	Port int    `json:"port,omitempty"`
	APK  string `json:"apk,omitempty"`
}

type UpdatesManifest struct {
	URL         string `json:"url,omitempty"`
	Channel     string `json:"channel,omitempty"`
	TrustedKeys string `json:"trusted_keys,omitempty"`
}

type Manifest struct {
	AppName            string                     `json:"app_name"`
	AppVersion         string                     `json:"app_version"`
	CKFrameworkVersion string                     `json:"ckframework_version"`
	CKLauncherVersion  string                     `json:"cklauncher_version"`
	ProtocolVersion    int                        `json:"protocol_version"`
	MinClientVersion   string                     `json:"min_client_version,omitempty"`
	Game               GameManifest               `json:"game"`
	Companion          CompanionManifest          `json:"companion,omitempty"`
	Updates            UpdatesManifest            `json:"updates,omitempty"`
	Discovery          ckdiscovery.ManifestConfig `json:"discovery,omitempty"`
	GitCommit          string                     `json:"git_commit,omitempty"`
	BuiltAt            string                     `json:"built_at"`
}

// BuildOptions controls a full monorepo release build (`corekiosk pack` / `ckpack build`).
type BuildOptions struct {
	ConfigPath    string
	RootOverride  string
	OutDir        string
	AppOverride   string
	VerOverride   string
	MinClient     string
	Debug         bool
	SkipTests     bool
	SkipCompanion bool
	SkipGame      bool
	SkipImport    bool
	GodotPath     string
	BuildAndroid  bool
	NoZip         bool
}

// RunCLI parses CLI flags and runs either full monorepo build mode or legacy stage mode.
func RunCLI(args []string) error {
	if len(args) > 0 && (args[0] == "build" || args[0] == "pack") {
		args = args[1:]
	}

	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	cfgPath := fs.String("config", "kiosk.json", "Path to activation monorepo kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory (auto-detected upwards when omitted)")
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

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *launcherExe != "" {
		return runLegacyStage(*appName, *appVer, *minClient, *launcherExe, *gameDir, *gameBin, *gameVer, *gameEngine, *outDir)
	}

	return Build(BuildOptions{
		ConfigPath:    *cfgPath,
		RootOverride:  *rootFlag,
		OutDir:        *outDir,
		AppOverride:   *appName,
		VerOverride:   *appVer,
		MinClient:     *minClient,
		Debug:         *debugMode,
		SkipTests:     *skipTests,
		SkipCompanion: *skipCompanion,
		SkipGame:      *skipGame,
		SkipImport:    *skipImport,
		GodotPath:     *godotFlag,
		BuildAndroid:  *buildAndroid,
		NoZip:         *noZip,
	})
}

// Build executes the full activation monorepo packaging pipeline.
func Build(opts BuildOptions) error {
	rootDir, cfg, err := LoadOrDetectConfig(opts.ConfigPath, opts.RootOverride)
	if err != nil {
		return err
	}
	if opts.AppOverride != "" {
		cfg.Name = opts.AppOverride
	}
	if opts.VerOverride != "" {
		cfg.Version = opts.VerOverride
	}
	if opts.MinClient != "" {
		cfg.MinClientVersion = opts.MinClient
	}

	outDirRel := opts.OutDir
	if outDirRel == "" {
		outDirRel = "./dist"
	}
	outBase := ResolvePath(rootDir, outDirRel)
	stageDir := filepath.Join(outBase, cfg.Name)
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("clean stage dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create stage dir: %w", err)
	}

	gitSha := gitShortRev(rootDir)
	modeName := "Release"
	if opts.Debug {
		modeName = "Debug"
	}

	step("Packaging %s v%s (%s)", cfg.Name, cfg.Version, modeName)
	fmt.Printf("  Root   : %s\n", rootDir)
	fmt.Printf("  Stage  : %s\n", stageDir)
	if gitSha != "" {
		fmt.Printf("  Commit : %s\n", gitSha)
	}

	// 1. Companion React Native Web (+ optional Android APK)
	var compManifest CompanionManifest
	compSrcDir := ResolvePath(rootDir, cfg.Companion.Dir)
	if !opts.SkipCompanion && cfg.Companion.Dir != "" && DirExists(compSrcDir) {
		step("1/4 Building Companion Web (%s)", cfg.Companion.Dir)
		if !DirExists(filepath.Join(compSrcDir, "node_modules")) {
			if err := runCmd(compSrcDir, nil, NpmBin(), "install"); err != nil {
				return fmt.Errorf("companion npm install: %w", err)
			}
		}
		if !opts.SkipTests && hasScript(compSrcDir, "typecheck") {
			if err := runCmd(compSrcDir, nil, NpmBin(), "run", "typecheck"); err != nil {
				return fmt.Errorf("companion typecheck failed: %w", err)
			}
		}
		if err := runCmd(compSrcDir, nil, NpxBin(), "expo", "export", "--platform", "web"); err != nil {
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
		compManifest = CompanionManifest{
			Dir:  "./companion",
			Port: port,
		}

		if opts.BuildAndroid {
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
	var gameManifest GameManifest
	gameSrcDir := ResolvePath(rootDir, cfg.Game.Dir)
	if !opts.SkipGame && cfg.Game.Dir != "" && FileExists(filepath.Join(gameSrcDir, "project.godot")) {
		step("2/4 Exporting Godot Game (%s / %s)", cfg.Game.Dir, cfg.Game.Preset)
		godotExe, godotVer, err := ResolveGodot(opts.GodotPath)
		if err != nil {
			return err
		}
		fmt.Printf("  Godot  : %s (%s)\n", godotVer, godotExe)

		if err := verifyGodotTemplates(gameSrcDir, cfg.Game.Preset, godotVer); err != nil {
			return err
		}

		cleanStaleGodotTempDLLs(gameSrcDir)

		importedDir := filepath.Join(gameSrcDir, ".godot", "imported")
		if !opts.SkipImport && !DirExists(importedDir) {
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
		if opts.Debug {
			exportFlag = "--export-debug"
		}
		if err := runCmd(gameSrcDir, nil, godotExe, "--headless", "--path", gameSrcDir, exportFlag, cfg.Game.Preset, gameOutExe); err != nil {
			return fmt.Errorf("godot %s failed: %w", exportFlag, err)
		}
		if !FileExists(gameOutExe) {
			return fmt.Errorf("godot export did not produce %s", gameOutExe)
		}

		for _, relDLL := range cfg.Game.RequiredDLLs {
			adjusted := relDLL
			if opts.Debug {
				adjusted = strings.ReplaceAll(adjusted, "template_release", "template_debug")
			}
			srcDLL := filepath.Join(gameSrcDir, filepath.FromSlash(adjusted))
			baseName := filepath.Base(srcDLL)
			if !fileExistsRecursive(gameStageDir, baseName) {
				if !FileExists(srcDLL) {
					return fmt.Errorf("missing required GDExtension DLL: %s", srcDLL)
				}
				if err := copyFile(srcDLL, filepath.Join(gameStageDir, baseName)); err != nil {
					return err
				}
			}
		}

		exeHash, _ := HashFile(gameOutExe)
		pckPath := strings.TrimSuffix(gameOutExe, filepath.Ext(gameOutExe)) + ".pck"
		pckHash, _ := HashFile(pckPath)

		gVer := cfg.Game.Version
		if gVer == "" {
			gVer = cfg.Version
		}
		gameManifest = GameManifest{
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
	serverSrcDir := ResolvePath(rootDir, cfg.Server.Dir)
	if !FileExists(filepath.Join(serverSrcDir, "go.mod")) {
		return fmt.Errorf("server go.mod not found in %s", serverSrcDir)
	}
	step("3/4 Building Go Server & Fyne Launcher (%s)", cfg.Server.Dir)
	if !opts.SkipTests {
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
	if !opts.Debug {
		ldflags = append(ldflags, "-s", "-w")
	}
	if runtime.GOOS == "windows" && winGUI {
		ldflags = append(ldflags, "-H=windowsgui")
	}

	goArgs := []string{"build", "-buildvcs=false"}
	if !opts.Debug {
		goArgs = append(goArgs, "-trimpath")
	}
	if len(ldflags) > 0 {
		goArgs = append(goArgs, "-ldflags", strings.Join(ldflags, " "))
	}
	goArgs = append(goArgs, "-o", launcherOut, ".")

	if err := runCmd(serverSrcDir, []string{"CGO_ENABLED=1"}, "go", goArgs...); err != nil {
		return fmt.Errorf("go build failed: %w", err)
	}

	// 4. Write kiosk.manifest.json, README.txt, and release .zip
	step("4/4 Writing kiosk.manifest.json and release bundle")
	manifest := Manifest{
		AppName:            cfg.Name,
		AppVersion:         cfg.Version,
		CKFrameworkVersion: ckframework.Version,
		CKLauncherVersion:  ckframework.Version,
		ProtocolVersion:    ckframework.ProtocolVersion,
		MinClientVersion:   cfg.MinClientVersion,
		Game:               gameManifest,
		Companion:          compManifest,
		Updates: UpdatesManifest{
			URL:         cfg.Updates.URL,
			Channel:     cfg.Updates.Channel,
			TrustedKeys: cfg.Updates.TrustedKeys,
		},
		Discovery: cfg.Discovery,
		GitCommit: gitSha,
		BuiltAt:   time.Now().UTC().Format(time.RFC3339),
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

	if opts.NoZip {
		fmt.Printf("\nStaged release ready at: %s\n", stageDir)
		return nil
	}

	archTag := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	zipName := packedZipName(cfg.Name, cfg.Version, archTag, time.Now())
	zipPath := filepath.Join(outBase, zipName)
	if err := createZip(stageDir, zipPath, cfg.Name); err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	zipHash, _ := HashFile(zipPath)
	zipStat, _ := os.Stat(zipPath)

	step("Release Package Complete")
	fmt.Printf("  Folder : %s\n", stageDir)
	fmt.Printf("  Zip    : %s (%.2f MB)\n", zipPath, float64(zipStat.Size())/(1024*1024))
	fmt.Printf("  SHA256 : %s\n", zipHash)
	return nil
}

// LoadOrDetectConfig locates `kiosk.json` (searching upwards from cwd when invoked inside a
// subdirectory such as `go -C ./server run ...`) and populates convention defaults.
func LoadOrDetectConfig(configPath, rootOverride string) (string, KioskProjectConfig, error) {
	var cfg KioskProjectConfig
	if configPath == "" {
		configPath = "kiosk.json"
	}

	var absRoot string
	var cfgFile string

	if rootOverride != "" {
		r, err := filepath.Abs(rootOverride)
		if err != nil {
			return "", cfg, err
		}
		absRoot = r
		if filepath.IsAbs(configPath) {
			cfgFile = configPath
		} else {
			cfgFile = filepath.Join(absRoot, configPath)
		}
	} else if filepath.IsAbs(configPath) {
		cfgFile = configPath
		absRoot = filepath.Dir(configPath)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", cfg, err
		}
		absRoot = cwd
		// Walk upwards up to 4 levels so `go -C ./server run ...` finds `../kiosk.json` automatically.
		cur := cwd
		for i := 0; i < 4; i++ {
			cand := filepath.Join(cur, configPath)
			if FileExists(cand) {
				cfgFile = cand
				absRoot = cur
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
		if cfgFile == "" {
			// Check if cwd itself is `./server` inside a conventional monorepo.
			if filepath.Base(cwd) == "server" && FileExists(filepath.Join(cwd, "go.mod")) {
				absRoot = filepath.Dir(cwd)
			}
			cfgFile = filepath.Join(absRoot, configPath)
		}
	}

	if raw, err := os.ReadFile(cfgFile); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", cfg, fmt.Errorf("parse %s: %w", cfgFile, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", cfg, err
	}

	if cfg.Name == "" {
		cfg.Name = strings.ToLower(filepath.Base(absRoot))
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.Server.Dir == "" {
		cfg.Server.Dir = "./server"
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = ":8080"
	}
	if cfg.Server.Exe == "" {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		cfg.Server.Exe = cfg.Name + ext
	}
	if cfg.Game.Dir == "" && DirExists(filepath.Join(absRoot, "game")) {
		cfg.Game.Dir = "./game"
	}
	if cfg.Game.Preset == "" {
		cfg.Game.Preset = "Windows Desktop"
	}
	if cfg.Game.Exe == "" {
		cfg.Game.Exe = cfg.Name + "-game.exe"
	}
	if cfg.Companion.Dir == "" && DirExists(filepath.Join(absRoot, "companion-rn")) {
		cfg.Companion.Dir = "./companion-rn"
	}
	if cfg.Companion.Port <= 0 {
		cfg.Companion.Port = 8090
	}
	return absRoot, cfg, nil
}

// ResolveGodot locates the Godot 4 executable, following Windows WinGet symlinks and
// preferring the synchronous `*_console.exe` wrapper when available.
func ResolveGodot(preferred string) (string, string, error) {
	candidates := []string{"godot_console", "godot4_console", "godot", "godot4"}
	var resolved string
	if preferred != "" {
		if FileExists(preferred) {
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
		return "", "", fmt.Errorf("godot executable not found on PATH (tried %s); pass -godot <path> or -no-game", strings.Join(candidates, ", "))
	}

	if realPath, err := filepath.EvalSymlinks(resolved); err == nil && realPath != "" {
		resolved = realPath
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(resolved), "_console.exe") && strings.HasSuffix(strings.ToLower(resolved), ".exe") {
		consoleSibling := resolved[:len(resolved)-4] + "_console.exe"
		if FileExists(consoleSibling) {
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

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func buildCompanionAndroid(compSrcDir, stageDir, appName string) (string, error) {
	androidDir := filepath.Join(compSrcDir, "android")
	if !DirExists(androidDir) {
		if err := runCmd(compSrcDir, nil, NpxBin(), "expo", "prebuild", "--platform", "android"); err != nil {
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
	if !FileExists(apkSrc) {
		return "", fmt.Errorf("expected release APK not found at %s", apkSrc)
	}
	apkName := appName + "-companion.apk"
	if err := copyFile(apkSrc, filepath.Join(stageDir, apkName)); err != nil {
		return "", err
	}
	return apkName, nil
}

func verifyGodotTemplates(gameDir, preset, godotTemplateVer string) error {
	presetsPath := filepath.Join(gameDir, "export_presets.cfg")
	if !FileExists(presetsPath) {
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
			if !FileExists(relTpl) {
				return fmt.Errorf("Godot export template not found at %s (install via Godot Editor -> Manage Export Templates, or pass -skip-game)", relTpl)
			}
		}
	}
	return nil
}

func writeQuickStartReadme(stageDir string, cfg KioskProjectConfig, comp CompanionManifest) error {
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

// packedZipName builds the release archive name. The build timestamp is always
// included so repeated packs of the same version never overwrite each other.
func packedZipName(appName, version, archTag string, at time.Time) string {
	return fmt.Sprintf("%s-v%s-%s-%s.zip", appName, version, archTag, at.UTC().Format("20060102-150405"))
}

func runLegacyStage(appName, appVer, minClient, launcherExe, gameDir, gameBin, gameVer, gameEngine, outDir string) error {
	if appName == "" {
		return fmt.Errorf("usage: ckpack build [-config kiosk.json] OR ckpack -app <name> -launcher <exe>")
	}
	if appVer == "" {
		appVer = "0.1.0"
	}
	dest := filepath.Join(outDir, appName)
	if err := os.MkdirAll(filepath.Join(dest, "game"), 0o755); err != nil {
		return err
	}
	launcherDst := filepath.Join(dest, appName+"-launcher"+filepath.Ext(launcherExe))
	if err := copyFile(launcherExe, launcherDst); err != nil {
		return err
	}
	gameHash := ""
	if gameDir != "" {
		if err := copyDir(gameDir, filepath.Join(dest, "game")); err != nil {
			return err
		}
		binPath := filepath.Join(dest, "game", gameBin)
		if h, err := HashFile(binPath); err == nil {
			gameHash = h
		}
	}
	manifest := Manifest{
		AppName:            appName,
		AppVersion:         appVer,
		CKFrameworkVersion: ckframework.Version,
		CKLauncherVersion:  ckframework.Version,
		ProtocolVersion:    ckframework.ProtocolVersion,
		MinClientVersion:   minClient,
		Game: GameManifest{
			Binary:  "./game/" + filepath.ToSlash(gameBin),
			Version: gameVer,
			Engine:  gameEngine,
			SHA256:  gameHash,
		},
		BuiltAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, "kiosk.manifest.json"), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("Packaged %s -> %s\n", appName, dest)
	return nil
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

func NpmBin() string {
	if runtime.GOOS == "windows" {
		return "npm.cmd"
	}
	return "npm"
}

func NpxBin() string {
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

func ResolvePath(root, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, filepath.FromSlash(p))
}

func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func DirExists(p string) bool {
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

func cleanStaleGodotTempDLLs(gameSrcDir string) {
	addonsDir := filepath.Join(gameSrcDir, "addons")
	if !DirExists(addonsDir) {
		return
	}
	_ = filepath.Walk(addonsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		lower := strings.ToLower(name)
		if strings.HasPrefix(name, "~") && (strings.HasSuffix(lower, ".dll") || strings.Contains(lower, ".dll~")) {
			if remErr := os.Remove(p); remErr != nil {
				fmt.Printf("  [note] %s is locked by an open Godot editor/game instance; continuing export\n", name)
			}
		}
		return nil
	})
}
