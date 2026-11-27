package ckserver

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/coder/websocket"
)

func osWriteTestFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestListenerRestartPreservesLoopbackBridgeAndSelfHeals(t *testing.T) {
	srv, err := New(newTestSpec(),
		WithDatabase(":memory:"),
		WithKeyFile(""),
		WithBridgeToken("relisten-tok"),
		WithDevMode(true),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bound, err := srv.Start(ctx, "0.0.0.0:0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Close(context.Background())

	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatalf("split bound addr %q: %v", bound, err)
	}
	baseURL := "http://127.0.0.1:" + port

	st1 := srv.ListenerStatus()
	if !st1.Listening || st1.Generation != 1 || st1.Port != port {
		t.Fatalf("unexpected initial listener status: %+v", st1)
	}

	// Connect a loopback Godot game bridge before rebinding.
	wsCtx, wsCancel := context.WithTimeout(ctx, 8*time.Second)
	defer wsCancel()
	bridgeConn, _, err := websocket.Dial(wsCtx, "ws://127.0.0.1:"+port+"/v1/bridge?token=relisten-tok", nil)
	if err != nil {
		t.Fatalf("dial loopback bridge: %v", err)
	}
	defer bridgeConn.CloseNow()

	writeBridge(t, wsCtx, bridgeConn, ckframework.BridgeMessage{
		T:              "ready",
		GameName:       "test-arcade",
		GameVersion:    "2.1.0",
		BridgeProtocol: ckframework.ProtocolVersion,
	})
	if hello := readBridge(t, wsCtx, bridgeConn); hello.T != "hello" {
		t.Fatalf("expected hello before relisten, got %+v", hello)
	}
	_ = readBridge(t, wsCtx, bridgeConn) // initial board_snapshot

	// 1. Trigger explicit RestartListener (simulating adapter reset / operator Relisten).
	if err := srv.RestartListener("unit_test_rebind"); err != nil {
		t.Fatalf("restart listener: %v", err)
	}

	st2 := srv.ListenerStatus()
	if !st2.Listening || st2.Generation != 2 || st2.Port != port {
		t.Fatalf("expected generation 2 on same port %s, got %+v", port, st2)
	}

	// Verify HTTP health responds on the rebound listener.
	var health ckframework.HealthResponse
	mustJSON(t, http.MethodGet, baseURL+"/v1/health", nil, nil, http.StatusOK, &health)

	// Verify the existing loopback game bridge stayed alive across listener restart!
	writeBridge(t, wsCtx, bridgeConn, ckframework.BridgeMessage{
		T:     "phase",
		Phase: ckframework.PhasePlaying,
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Hub().Snapshot().Phase == ckframework.PhasePlaying {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := srv.Hub().Snapshot().Phase; got != ckframework.PhasePlaying {
		t.Fatalf("expected loopback bridge to stay live and set phase=playing, got %q", got)
	}

	// 2. Simulate an unexpected OS socket crash by closing the underlying net.Listener directly
	// without going through RestartListener/Shutdown, and verify self-healing rebinds automatically!
	srv.listener.mu.RLock()
	rawLn := srv.listener.curLn
	srv.listener.mu.RUnlock()
	_ = rawLn.Close()

	healDeadline := time.Now().Add(4 * time.Second)
	healed := false
	for time.Now().Before(healDeadline) {
		st := srv.ListenerStatus()
		if st.Listening && st.Generation >= 3 {
			resp, err := http.Get(baseURL + "/v1/health")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					healed = true
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healed {
		t.Fatalf("listener did not self-heal after raw socket drop; status=%+v", srv.ListenerStatus())
	}
}

func TestConnTrackerPrunesVanishedLANWhilePreservingLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ct := newConnTracker()
	tl := &trackedListener{Listener: ln, tracker: ct}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := tl.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	srvConn := <-accepted
	defer srvConn.Close()

	// Loopback connection must NEVER be pruned even when validLANIPs is empty.
	if pruned := ct.pruneStaleLAN(nil); pruned != 0 {
		t.Fatalf("expected 0 loopback conns pruned, got %d", pruned)
	}
}

func TestCompanionWebHostServesSPAAndAssets(t *testing.T) {
	dir := t.TempDir()
	if err := osWriteTestFile(dir+"/index.html", "<!doctype html><html><body>Joyland Companion</body></html>"); err != nil {
		t.Fatal(err)
	}
	if err := osWriteTestFile(dir+"/_expo/static/js/web/app.js", "console.log('ok');"); err != nil {
		t.Fatal(err)
	}

	srv, err := New(newTestSpec(), WithDatabase(":memory:"), WithKeyFile(""))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer srv.Close(context.Background())

	if _, err := srv.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	compBound, err := srv.StartCompanionWeb(ctx, "127.0.0.1:0", dir)
	if err != nil {
		t.Fatalf("start companion web: %v", err)
	}

	// Root returns index.html
	resp, err := http.Get("http://" + compBound + "/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("root GET failed: err=%v resp=%v", err, resp)
	}
	_ = resp.Body.Close()

	// Hashed asset returns 200 + immutable Cache-Control
	assetResp, err := http.Get("http://" + compBound + "/_expo/static/js/web/app.js")
	if err != nil || assetResp.StatusCode != http.StatusOK {
		t.Fatalf("asset GET failed: err=%v resp=%v", err, assetResp)
	}
	_ = assetResp.Body.Close()
	if assetResp.Header.Get("Cache-Control") == "" {
		t.Fatalf("expected Cache-Control header on _expo asset")
	}

	// Missing .js returns 404 (not index.html)
	missResp, err := http.Get("http://" + compBound + "/_expo/static/js/web/missing.js")
	if err != nil || missResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing asset, got err=%v resp=%v", err, missResp)
	}
	_ = missResp.Body.Close()

	// Client-side SPA route without extension falls back to index.html (200)
	spaResp, err := http.Get("http://" + compBound + "/operator/controls")
	if err != nil || spaResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 SPA fallback, got err=%v resp=%v", err, spaResp)
	}
	_ = spaResp.Body.Close()
}
