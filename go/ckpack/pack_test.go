package ckpack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackedZipNameIncludesTimestamp(t *testing.T) {
	at := time.Date(2026, 11, 28, 13, 45, 12, 0, time.UTC)
	got := packedZipName("joyland", "0.2.0", "windows-amd64", at)
	want := "joyland-v0.2.0-windows-amd64-20261128-134512.zip"
	if got != want {
		t.Fatalf("packedZipName: want %q, got %q", want, got)
	}
}

func TestLoadOrDetectConfigWalksUpwardsFromServerSubdir(t *testing.T) {
	root := t.TempDir()
	srvDir := filepath.Join(root, "server")
	if err := os.MkdirAll(srvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kiosk.json"), []byte(`{"name":"demo-kiosk","version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srvDir, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(srvDir); err != nil {
		t.Fatal(err)
	}

	gotRoot, cfg, err := LoadOrDetectConfig("kiosk.json", "")
	if err != nil {
		t.Fatalf("LoadOrDetectConfig: %v", err)
	}
	if gotRoot != root || cfg.Name != "demo-kiosk" || cfg.Version != "1.2.3" {
		t.Fatalf("unexpected resolved root=%q cfg=%+v", gotRoot, cfg)
	}
}
