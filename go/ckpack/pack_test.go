package ckpack

import (
	"os"
	"path/filepath"
	"testing"
)

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
