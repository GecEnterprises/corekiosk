package cklauncher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCountdownLaunchNow(t *testing.T) {
	cd := NewCountdown(30, false)
	cd.Pause()
	require.True(t, cd.State().Paused)
	cd.Resume()
	go func() {
		time.Sleep(25 * time.Millisecond)
		cd.LaunchNow()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.True(t, cd.Wait(ctx))
	require.True(t, cd.State().Launched)
}

func TestHashFileAndManifest(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "game.exe")
	require.NoError(t, os.WriteFile(bin, []byte("mock-game-binary"), 0o644))
	h1, err := HashFile(bin)
	require.NoError(t, err)
	require.Len(t, h1, 64)
}
