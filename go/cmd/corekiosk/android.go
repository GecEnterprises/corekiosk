package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	case "dev-apk":
		return runAndroidDevAPK(rest)
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
  corekiosk android avd [flags]        Boot AVD (if needed), setup adb reverse, and run app via Expo
  corekiosk android dev-apk [flags]    Build debug APK directly via Gradle (no Expo) and install into AVD
  corekiosk android apk [flags]        Build standalone Android APK via Gradle into ./dist/ (optional -install)`)
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

func androidEnv(compDir string) []string {
	sdk := findAndroidSDK()
	if sdk == "" {
		return nil
	}
	androidDir := filepath.Join(compDir, "android")
	if ckpack.DirExists(androidDir) {
		escaped := strings.ReplaceAll(sdk, `\`, `\\`)
		_ = os.WriteFile(filepath.Join(androidDir, "local.properties"), []byte("sdk.dir="+escaped+"\n"), 0o644)
	}
	return []string{
		"ANDROID_HOME=" + sdk,
		"ANDROID_SDK_ROOT=" + sdk,
	}
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
	if err := runStreaming(compDir, androidEnv(compDir), ckpack.NpxBin(), prebuildArgs...); err != nil {
		return err
	}
	_ = androidEnv(compDir)

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

	if err := ensureAndroidDevice(adbBin, *avdName); err != nil {
		return err
	}

	setupADBReverse(adbBin, port, *metroPort)

	runArgs := []string{"expo", "run:android"}
	if *release {
		runArgs = append(runArgs, "--variant", "release")
	}
	fmt.Printf("[android] running npx %s\n", strings.Join(runArgs, " "))
	return runStreaming(compDir, androidEnv(compDir), ckpack.NpxBin(), runArgs...)
}

type apkCommandDefaults struct {
	name           string
	debug          bool
	install        bool
	skipAutoPrebld bool
}

func runAndroidAPK(args []string) error {
	return runAndroidAPKWithDefaults(args, apkCommandDefaults{
		name:           "android apk",
		debug:          false,
		install:        false,
		skipAutoPrebld: false,
	})
}

func runAndroidDevAPK(args []string) error {
	return runAndroidAPKWithDefaults(args, apkCommandDefaults{
		name:           "android dev-apk",
		debug:          true,
		install:        true,
		skipAutoPrebld: true,
	})
}

func runAndroidAPKWithDefaults(args []string, defaults apkCommandDefaults) error {
	fs := flag.NewFlagSet(defaults.name, flag.ContinueOnError)
	configPath := fs.String("config", "kiosk.json", "Path to kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory")
	debug := fs.Bool("debug", defaults.debug, "Build debug APK instead of release")
	clean := fs.Bool("clean", false, "Run clean expo prebuild before Gradle build")
	prebuild := fs.Bool("prebuild", false, "Run expo prebuild before Gradle build if needed")
	outPath := fs.String("out", "", "Output APK path (default: ./dist/<app>-companion-<variant>.apk)")
	install := fs.Bool("install", defaults.install, "Boot AVD if needed, adb reverse, install APK, and launch app")
	avdName := fs.String("avd", "", "AVD name to boot if no device is running (used with -install)")
	apiPort := fs.String("port", "", "Server API port to reverse-forward via adb (default from kiosk.json or 8080)")
	metroPort := fs.String("metro-port", "8081", "Metro bundler port to reverse-forward via adb")
	arch := fs.String("arch", "", "Override reactNativeArchitectures for Gradle (e.g. x86_64; auto-detected from AVD on -install)")
	allABIs := fs.Bool("all-abis", false, "Build all ABIs from gradle.properties even when installing to an AVD")
	noLaunch := fs.Bool("no-launch", false, "Do not launch the app after installing")
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
	if *clean || *prebuild || (!defaults.skipAutoPrebld && !ckpack.DirExists(androidDir)) {
		prebuildArgs := []string{"expo", "prebuild", "--platform", "android"}
		if *clean {
			prebuildArgs = append(prebuildArgs, "--clean")
		}
		fmt.Printf("[android] running npx %s\n", strings.Join(prebuildArgs, " "))
		if err := runStreaming(compDir, androidEnv(compDir), ckpack.NpxBin(), prebuildArgs...); err != nil {
			return err
		}
	}
	if !ckpack.DirExists(androidDir) {
		return fmt.Errorf("android project directory not found at %s (run 'corekiosk android prebuild' once first, or pass -prebuild)", androidDir)
	}

	var adbBin string
	targetArch := strings.TrimSpace(*arch)
	if *install {
		adbBin = findAndroidTool("platform-tools", "adb")
		if adbBin == "" {
			return fmt.Errorf("adb not found in PATH or ANDROID_HOME/platform-tools")
		}
		if err := ensureAndroidDevice(adbBin, *avdName); err != nil {
			return err
		}
		if targetArch == "" && !*allABIs {
			if abi := detectAndroidDeviceABI(adbBin); abi != "" {
				targetArch = abi
				fmt.Printf("[android] detected connected device ABI: %s\n", targetArch)
			}
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
	gradleArgs := []string{gradleTask}
	if targetArch != "" {
		gradleArgs = append(gradleArgs, "-PreactNativeArchitectures="+targetArch)
	}
	fmt.Printf("[android] building %s APK directly via Gradle: %s %s\n", variant, gradlew, strings.Join(gradleArgs, " "))
	if err := runStreaming(androidDir, androidEnv(compDir), gradlew, gradleArgs...); err != nil {
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

	if *install {
		port := resolveServerPort(*apiPort, cfg)
		setupADBReverse(adbBin, port, *metroPort)
		fmt.Printf("[android] installing %s onto AVD/device...\n", dest)
		if err := runStreaming(rootDir, nil, adbBin, "install", "-r", "-d", dest); err != nil {
			return fmt.Errorf("adb install failed: %w", err)
		}
		if !*noLaunch {
			if pkg := detectAndroidPackageID(compDir, androidDir); pkg != "" {
				_ = exec.Command(adbBin, "shell", "am", "force-stop", pkg).Run()
				fmt.Printf("[android] launching %s/.MainActivity...\n", pkg)
				if err := runStreaming(rootDir, nil, adbBin, "shell", "am", "start", "-n", pkg+"/.MainActivity"); err != nil {
					_ = runStreaming(rootDir, nil, adbBin, "shell", "monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1")
				}
			} else {
				fmt.Println("[android] installed APK (could not detect applicationId to auto-launch)")
			}
		}
	}
	return nil
}

func resolveServerPort(apiPortFlag string, cfg ckpack.KioskProjectConfig) string {
	if p := strings.TrimSpace(apiPortFlag); p != "" {
		return p
	}
	if cfg.Server.Listen != "" {
		if _, p, err := net.SplitHostPort(cfg.Server.Listen); err == nil && p != "" {
			return p
		}
	}
	return "8080"
}

func ensureAndroidDevice(adbBin, avdName string) error {
	if hasConnectedAndroidDevice(adbBin) {
		return nil
	}
	emuBin := findAndroidTool("emulator", "emulator")
	if emuBin == "" {
		return fmt.Errorf("no Android device connected and emulator binary not found")
	}
	chosen := strings.TrimSpace(avdName)
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
	return waitForAndroidBoot(adbBin, 120*time.Second)
}

func detectAndroidDeviceABI(adbBin string) string {
	out, err := exec.Command(adbBin, "shell", "getprop", "ro.product.cpu.abi").Output()
	if err != nil {
		return ""
	}
	abi := strings.TrimSpace(string(out))
	switch abi {
	case "armeabi-v7a", "arm64-v8a", "x86", "x86_64":
		return abi
	default:
		return ""
	}
}

var appIDRegex = regexp.MustCompile(`applicationId\s+['"]([^'"]+)['"]`)

func detectAndroidPackageID(compDir, androidDir string) string {
	if raw, err := os.ReadFile(filepath.Join(androidDir, "app", "build.gradle")); err == nil {
		if m := appIDRegex.FindSubmatch(raw); len(m) == 2 {
			return strings.TrimSpace(string(m[1]))
		}
	}
	if raw, err := os.ReadFile(filepath.Join(compDir, "app.json")); err == nil {
		var doc struct {
			Expo struct {
				Android struct {
					Package string `json:"package"`
				} `json:"android"`
			} `json:"expo"`
		}
		if json.Unmarshal(raw, &doc) == nil && strings.TrimSpace(doc.Expo.Android.Package) != "" {
			return strings.TrimSpace(doc.Expo.Android.Package)
		}
	}
	return ""
}

func setupADBReverse(adbBin, apiPort, metroPort string) {
	if adbBin == "" || !hasConnectedAndroidDevice(adbBin) {
		return
	}
	for _, p := range []string{apiPort, metroPort} {
		if strings.TrimSpace(p) == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		err := exec.CommandContext(ctx, adbBin, "reverse", "tcp:"+p, "tcp:"+p).Run()
		cancel()
		if err == nil {
			fmt.Printf("[android] adb reverse tcp:%s -> tcp:%s\n", p, p)
		}
	}
}

func hasConnectedAndroidDevice(adbBin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, adbBin, "devices").Output()
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
