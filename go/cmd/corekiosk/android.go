package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckpack"
)

func runAndroid(args []string) error {
	if len(args) == 0 {
		printAndroidUsage()
		return nil
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "prebuild":
		return runAndroidPrebuild(rest)
	case "avd":
		return runAndroidAVD(rest)
	case "apk":
		return runAndroidAPK(rest)
	case "help", "-h", "--help":
		printAndroidUsage()
		return nil
	default:
		printAndroidUsage()
		return fmt.Errorf("unknown android subcommand %q", sub)
	}
}

func printAndroidUsage() {
	fmt.Println(`corekiosk android — Android Studio, AVD, and APK tooling

Usage:
  corekiosk android prebuild [flags]   Generate ./companion-rn/android for Android Studio
  corekiosk android avd [flags]        Boot AVD (if needed), setup adb reverse, and run app
  corekiosk android apk [flags]        Build standalone Android APK into ./dist/`)
}

func loadCompanionDir(configPath, rootFlag string) (string, ckpack.KioskProjectConfig, string, error) {
	rootDir, cfg, err := ckpack.LoadOrDetectConfig(configPath, rootFlag)
	if err != nil {
		return "", cfg, "", err
	}
	if cfg.Companion.Dir == "" {
		return "", cfg, "", fmt.Errorf("companion.dir is not configured in kiosk.json")
	}
	compDir := ckpack.ResolvePath(rootDir, cfg.Companion.Dir)
	if !ckpack.DirExists(compDir) {
		return "", cfg, "", fmt.Errorf("companion directory not found at %s", compDir)
	}
	return rootDir, cfg, compDir, nil
}

func ensureNodeModules(compDir string) error {
	if ckpack.DirExists(filepath.Join(compDir, "node_modules")) {
		return nil
	}
	fmt.Printf("[android] installing node_modules in %s...\n", compDir)
	return runStreaming(compDir, nil, ckpack.NpmBin(), "install")
}

func runAndroidPrebuild(args []string) error {
	fs := flag.NewFlagSet("android prebuild", flag.ContinueOnError)
	configPath := fs.String("config", "kiosk.json", "Path to kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory")
	clean := fs.Bool("clean", false, "Pass --clean to expo prebuild")
	openStudio := fs.Bool("studio", false, "Open generated android/ folder in Android Studio")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, _, compDir, err := loadCompanionDir(*configPath, *rootFlag)
	if err != nil {
		return err
	}
	if err := ensureNodeModules(compDir); err != nil {
		return err
	}

	prebuildArgs := []string{"expo", "prebuild", "--platform", "android"}
	if *clean {
		prebuildArgs = append(prebuildArgs, "--clean")
	}
	fmt.Printf("[android] running npx %s\n", strings.Join(prebuildArgs, " "))
	if err := runStreaming(compDir, nil, ckpack.NpxBin(), prebuildArgs...); err != nil {
		return err
	}

	androidDir := filepath.Join(compDir, "android")
	fmt.Printf("[android] Android Studio project ready at: %s\n", androidDir)
	if *openStudio {
		if studio := findAndroidStudio(); studio != "" {
			fmt.Printf("[android] launching Android Studio (%s)...\n", studio)
			_ = exec.Command(studio, androidDir).Start()
		} else {
			fmt.Println("[android] Android Studio executable not found; open the folder above manually.")
		}
	}
	return nil
}

func runAndroidAVD(args []string) error {
	fs := flag.NewFlagSet("android avd", flag.ContinueOnError)
	configPath := fs.String("config", "kiosk.json", "Path to kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory")
	avdName := fs.String("avd", "", "AVD name to boot if no device is running")
	apiPort := fs.String("port", "", "Server API port to reverse-forward via adb (default from kiosk.json or 8080)")
	metroPort := fs.String("metro-port", "8081", "Metro bundler port to reverse-forward via adb")
	release := fs.Bool("release", false, "Build & install release variant instead of debug")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, cfg, compDir, err := loadCompanionDir(*configPath, *rootFlag)
	if err != nil {
		return err
	}
	if err := ensureNodeModules(compDir); err != nil {
		return err
	}

	port := *apiPort
	if port == "" {
		if cfg.Server.Listen != "" {
			if _, p, err := net.SplitHostPort(cfg.Server.Listen); err == nil && p != "" {
				port = p
			}
		}
		if port == "" {
			port = "8080"
		}
	}

	adbBin := findAndroidTool("platform-tools", "adb")
	if adbBin == "" {
		return fmt.Errorf("adb not found in PATH or ANDROID_HOME/platform-tools")
	}

	if !hasConnectedAndroidDevice(adbBin) {
		emuBin := findAndroidTool("emulator", "emulator")
		if emuBin == "" {
			return fmt.Errorf("no Android device connected and emulator binary not found")
		}
		chosen := strings.TrimSpace(*avdName)
		if chosen == "" {
			out, err := exec.Command(emuBin, "-list-avds").Output()
			if err != nil {
				return fmt.Errorf("list AVDs: %w", err)
			}
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					chosen = line
					break
				}
			}
		}
		if chosen == "" {
			return fmt.Errorf("no AVDs configured; create one in Android Studio Device Manager")
		}
		fmt.Printf("[android] booting AVD %q...\n", chosen)
		cmd := exec.Command(emuBin, "-avd", chosen)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start emulator: %w", err)
		}
		if err := waitForAndroidBoot(adbBin, 120*time.Second); err != nil {
			return err
		}
	}

	setupADBReverse(adbBin, port, *metroPort)

	runArgs := []string{"expo", "run:android"}
	if *release {
		runArgs = append(runArgs, "--variant", "release")
	}
	fmt.Printf("[android] running npx %s\n", strings.Join(runArgs, " "))
	return runStreaming(compDir, nil, ckpack.NpxBin(), runArgs...)
}

func runAndroidAPK(args []string) error {
	fs := flag.NewFlagSet("android apk", flag.ContinueOnError)
	configPath := fs.String("config", "kiosk.json", "Path to kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory")
	debug := fs.Bool("debug", false, "Build debug APK instead of release")
	clean := fs.Bool("clean", false, "Run clean expo prebuild before Gradle build")
	outPath := fs.String("out", "", "Output APK path (default: ./dist/<app>-companion-<variant>.apk)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rootDir, cfg, compDir, err := loadCompanionDir(*configPath, *rootFlag)
	if err != nil {
		return err
	}
	if err := ensureNodeModules(compDir); err != nil {
		return err
	}

	androidDir := filepath.Join(compDir, "android")
	if *clean || !ckpack.DirExists(androidDir) {
		prebuildArgs := []string{"expo", "prebuild", "--platform", "android"}
		if *clean {
			prebuildArgs = append(prebuildArgs, "--clean")
		}
		fmt.Printf("[android] running npx %s\n", strings.Join(prebuildArgs, " "))
		if err := runStreaming(compDir, nil, ckpack.NpxBin(), prebuildArgs...); err != nil {
			return err
		}
	}

	variant := "release"
	gradleTask := "assembleRelease"
	if *debug {
		variant = "debug"
		gradleTask = "assembleDebug"
	}

	gradlew := "./gradlew"
	if runtime.GOOS == "windows" {
		gradlew = filepath.Join(androidDir, "gradlew.bat")
	}
	fmt.Printf("[android] building %s APK via %s %s...\n", variant, gradlew, gradleTask)
	if err := runStreaming(androidDir, nil, gradlew, gradleTask); err != nil {
		return err
	}

	srcAPK := filepath.Join(androidDir, "app", "build", "outputs", "apk", variant, fmt.Sprintf("app-%s.apk", variant))
	if !ckpack.FileExists(srcAPK) {
		return fmt.Errorf("expected APK not found at %s", srcAPK)
	}

	dest := *outPath
	if dest == "" {
		appName := sanitizeName(cfg.Name)
		if appName == "" {
			appName = "corekiosk"
		}
		dest = filepath.Join(rootDir, "dist", fmt.Sprintf("%s-companion-%s.apk", appName, variant))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := copyFile(srcAPK, dest); err != nil {
		return err
	}
	fmt.Printf("[android] APK ready: %s\n", dest)
	return nil
}

func setupADBReverse(adbBin, apiPort, metroPort string) {
	if adbBin == "" || !hasConnectedAndroidDevice(adbBin) {
		return
	}
	for _, p := range []string{apiPort, metroPort} {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := exec.Command(adbBin, "reverse", "tcp:"+p, "tcp:"+p).Run(); err == nil {
			fmt.Printf("[android] adb reverse tcp:%s -> tcp:%s\n", p, p)
		}
	}
}

func hasConnectedAndroidDevice(adbBin string) bool {
	out, err := exec.Command(adbBin, "devices").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "device" {
			return true
		}
	}
	return false
}

func waitForAndroidBoot(adbBin string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = exec.CommandContext(ctx, adbBin, "wait-for-device").Run()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for AVD boot")
		}
		out, err := exec.CommandContext(ctx, adbBin, "shell", "getprop", "sys.boot_completed").Output()
		if err == nil && bytes.TrimSpace(out) != nil && strings.TrimSpace(string(out)) == "1" {
			return nil
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for AVD boot")
}

func findAndroidSDK() string {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" && ckpack.DirExists(v) {
			return v
		}
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			p := filepath.Join(local, "Android", "Sdk")
			if ckpack.DirExists(p) {
				return p
			}
		}
	}
	return ""
}

func findAndroidTool(subDir, name string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	if p, err := exec.LookPath(name + ext); err == nil {
		return p
	}
	if sdk := findAndroidSDK(); sdk != "" {
		cand := filepath.Join(sdk, subDir, name+ext)
		if ckpack.FileExists(cand) {
			return cand
		}
	}
	return ""
}

func findAndroidStudio() string {
	if p, err := exec.LookPath("studio64.exe"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, cand := range []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Android", "Android Studio", "bin", "studio64.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Android Studio", "bin", "studio64.exe"),
		} {
			if ckpack.FileExists(cand) {
				return cand
			}
		}
	}
	return ""
}

func runStreaming(dir string, extraEnv []string, bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
