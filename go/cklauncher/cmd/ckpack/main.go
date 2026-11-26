package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cklauncher"
)

func main() {
	appName := flag.String("app", "", "Activation app name (required)")
	appVer := flag.String("app-version", "0.1.0", "Activation SemVer")
	minClient := flag.String("min-client-version", "", "Optional minimum tablet client SemVer")
	launcherExe := flag.String("launcher", "", "Path to compiled launcher executable (required)")
	gameDir := flag.String("game-dir", "", "Path to exported game directory")
	gameBin := flag.String("game-bin", "game.exe", "Relative path to game binary inside game/")
	gameVer := flag.String("game-version", "0.1.0", "Game binary SemVer")
	gameEngine := flag.String("engine", "godot", "Game engine identifier (e.g. godot-4.4)")
	outDir := flag.String("out", "./dist", "Output directory")
	flag.Parse()

	if *appName == "" || *launcherExe == "" {
		fmt.Fprintln(os.Stderr, "usage: ckpack -app <name> -launcher <path-to-launcher.exe> [-game-dir <dir>] [-game-bin <exe>] [-game-version <semver>]")
		os.Exit(2)
	}

	dest := filepath.Join(*outDir, *appName)
	if err := os.MkdirAll(filepath.Join(dest, "game"), 0o755); err != nil {
		fatal(err)
	}

	launcherDst := filepath.Join(dest, *appName+"-launcher"+filepath.Ext(*launcherExe))
	if err := copyFile(*launcherExe, launcherDst); err != nil {
		fatal(err)
	}

	gameHash := ""
	if *gameDir != "" {
		if err := copyDir(*gameDir, filepath.Join(dest, "game")); err != nil {
			fatal(err)
		}
		binPath := filepath.Join(dest, "game", *gameBin)
		if h, err := cklauncher.HashFile(binPath); err == nil {
			gameHash = h
		}
	}

	manifest := cklauncher.Manifest{
		AppName:            *appName,
		AppVersion:         *appVer,
		CKFrameworkVersion: ckframework.Version,
		CKLauncherVersion:  cklauncher.FullVersion(),
		ProtocolVersion:    ckframework.ProtocolVersion,
		MinClientVersion:   *minClient,
		Game: cklauncher.GameManifest{
			Binary:  "./game/" + filepath.ToSlash(*gameBin),
			Version: *gameVer,
			Engine:  *gameEngine,
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
	fmt.Printf("Packaged %s -> %s (framework=%s launcher=%s game=%s)\n", *appName, dest, manifest.CKFrameworkVersion, manifest.CKLauncherVersion, manifest.Game.Version)
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

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "ckpack error: %v\n", err)
	os.Exit(1)
}
