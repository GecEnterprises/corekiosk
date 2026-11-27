package ckupdater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseKeySignVerifyAndPolicyNextRestart(t *testing.T) {
	tmp := t.TempDir()
	kf, err := GenerateReleaseKeyPair()
	require.NoError(t, err)
	keyPath := filepath.Join(tmp, "release.key")
	require.NoError(t, SaveReleaseKeyFile(keyPath, kf))

	priv, _, keyID, err := LoadReleaseKeyFile(keyPath)
	require.NoError(t, err)
	require.Equal(t, kf.KeyID, keyID)

	trusted, err := ParseTrustedKeys(kf.PublicKey)
	require.NoError(t, err)

	// Build a minimal zip bundle for game target
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	fw, err := zw.Create("game/game.pck")
	require.NoError(t, err)
	_, err = fw.Write([]byte("new-game-pck-bytes"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	sum := sha256.Sum256(zipBuf.Bytes())
	shaHex := hex.EncodeToString(sum[:])

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundle.zip" {
			_, _ = w.Write(zipBuf.Bytes())
			return
		}
		art := TargetArtifact{
			Target:    TargetGame,
			Version:   "0.3.1",
			Policy:    PolicyNextRestart,
			URL:       srvURL + "/bundle.zip",
			SHA256:    shaHex,
			SizeBytes: int64(zipBuf.Len()),
		}
		SignArtifact(priv, "joyland-essential", &art)
		_ = json.NewEncoder(w).Encode(LatestUpdatesResponse{
			App:     "joyland-essential",
			Channel: "stable",
			Targets: map[string]TargetArtifact{string(TargetGame): art},
		})
	}))
	defer srv.Close()
	srvURL = srv.URL

	appDir := filepath.Join(tmp, "app")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "game"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "game", "game.pck"), []byte("old-pck"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "data", "kiosk.db"), []byte("sqlite-db"), 0o644))

	mgr := NewManager(Config{
		EndpointURL: srv.URL + "/latest",
		AppName:     "joyland-essential",
		Channel:     "stable",
		TrustedKeys: trusted,
		BaseDir:     appDir,
	})
	require.NoError(t, mgr.CheckAndHandle(context.Background()))

	// PolicyNextRestart should stage into updates/pending without touching game/game.pck yet
	oldBytes, err := os.ReadFile(filepath.Join(appDir, "game", "game.pck"))
	require.NoError(t, err)
	require.Equal(t, "old-pck", string(oldBytes))

	// On next boot, ApplyPendingOnBoot swaps game/game.pck while preserving data/kiosk.db
	require.NoError(t, mgr.ApplyPendingOnBoot())
	newBytes, err := os.ReadFile(filepath.Join(appDir, "game", "game.pck"))
	require.NoError(t, err)
	require.Equal(t, "new-game-pck-bytes", string(newBytes))

	dbBytes, err := os.ReadFile(filepath.Join(appDir, "data", "kiosk.db"))
	require.NoError(t, err)
	require.Equal(t, "sqlite-db", string(dbBytes))

	// Tampered signature must fail
	badArt := TargetArtifact{Target: TargetGame, Version: "0.3.2", Policy: PolicyImmediate, SHA256: shaHex, KeyID: keyID, Sig: "bad"}
	require.Error(t, VerifyArtifact(map[string]ed25519.PublicKey{}, "joyland-essential", badArt))
	require.Error(t, VerifyArtifact(trusted, "joyland-essential", badArt))
}

func TestPolicyWhenIdleWaitsForIdleAndApplies(t *testing.T) {
	tmp := t.TempDir()
	kf, err := GenerateReleaseKeyPair()
	require.NoError(t, err)
	priv, _, _, err := LoadReleaseKeyFileFromStruct(kf)
	require.NoError(t, err)
	trusted, err := ParseTrustedKeys(kf.PublicKey)
	require.NoError(t, err)

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	fw, err := zw.Create("game/game.pck")
	require.NoError(t, err)
	_, err = fw.Write([]byte("idle-updated-pck"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	sum := sha256.Sum256(zipBuf.Bytes())
	shaHex := hex.EncodeToString(sum[:])

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundle.zip" {
			_, _ = w.Write(zipBuf.Bytes())
			return
		}
		art := TargetArtifact{
			Target:    TargetGame,
			Version:   "0.4.0",
			Policy:    PolicyWhenIdle,
			URL:       srvURL + "/bundle.zip",
			SHA256:    shaHex,
			SizeBytes: int64(zipBuf.Len()),
		}
		SignArtifact(priv, "joyland-essential", &art)
		_ = json.NewEncoder(w).Encode(LatestUpdatesResponse{
			App:     "joyland-essential",
			Channel: "stable",
			Targets: map[string]TargetArtifact{string(TargetGame): art},
		})
	}))
	defer srv.Close()
	srvURL = srv.URL

	appDir := filepath.Join(tmp, "app")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "game"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "game", "game.pck"), []byte("old"), 0o644))

	idle := false
	stopped := false
	restarted := false
	mgr := NewManager(Config{
		EndpointURL: srv.URL + "/latest",
		AppName:     "joyland-essential",
		TrustedKeys: trusted,
		BaseDir:     appDir,
		IsIdle:      func() bool { return idle },
		StopGame:    func() { stopped = true },
		RestartGame: func() { restarted = true },
	})

	done := make(chan error, 1)
	go func() {
		done <- mgr.CheckAndHandle(context.Background())
	}()

	// Flip to idle after a short delay; CheckAndHandle should then apply and restart game
	idle = true
	require.NoError(t, <-done)
	require.True(t, stopped)
	require.True(t, restarted)

	got, err := os.ReadFile(filepath.Join(appDir, "game", "game.pck"))
	require.NoError(t, err)
	require.Equal(t, "idle-updated-pck", string(got))
}
