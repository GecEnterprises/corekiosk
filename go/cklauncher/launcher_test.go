package cklauncher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCountdownLaunchNow(t *testing.T) {
	cd := NewCountdown(30, false)
	cd.Pause()
	if !cd.State().Paused {
		t.Fatalf("expected paused")
	}
	cd.Resume()
	go func() {
		time.Sleep(25 * time.Millisecond)
		cd.LaunchNow()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !cd.Wait(ctx) {
		t.Fatalf("expected countdown to proceed on LaunchNow")
	}
	if !cd.State().Launched {
		t.Fatalf("expected launched state")
	}
}

func TestHashFileAndManifest(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "game.exe")
	if err := os.WriteFile(bin, []byte("mock-game-binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := HashFile(bin)
	if err != nil || len(h1) != 64 {
		t.Fatalf("unexpected sha256: %q %v", h1, err)
	}
}
