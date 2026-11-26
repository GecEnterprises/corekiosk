package ckserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/coder/websocket"
)

func newTestSpec() *ckframework.AppSpec {
	return ckframework.NewApp("test-arcade", "1.0.0").
		WithForm(
			ckframework.TextField("name", "Name").RequiredField().MaxLength(24),
			ckframework.EmailField("email", "Email").RequiredField(),
		).
		WithSettings(
			ckframework.EnumSetting("display.mode", "Display", "Mode", []ckframework.EnumOption{
				{Value: "square", Label: "Square"},
				{Value: "centered", Label: "LED Centered"},
			}, "square"),
			ckframework.BoolSetting("debug.hitboxes", "Display", "Hitboxes", false).RuntimeOnly(),
		).
		WithActions(
			ckframework.Action("game.seed_demo", "Game", "Seed Demo").TargetGame(),
		).
		WithBoards(
			ckframework.NewBoard("top_scores", "Leaderboard").
				WithColumns(
					ckframework.RankColumn("#"),
					ckframework.FieldColumn("name", "Player"),
					ckframework.NumberColumn("score", "Score"),
				).
				SortBy("score", ckframework.SortDesc).
				WithRowDelete(true).
				WithBoardClear(true),
		)
}

func TestEndToEndKioskLifecycle(t *testing.T) {
	srv, err := New(newTestSpec(),
		WithDatabase(":memory:"),
		WithKeyFile(""),
		WithBridgeToken("test-bridge-token"),
		WithVersions("0.1.0-test", "2.0.0"),
		WithDevMode(true),
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer srv.Close(context.Background())

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Health & Pairing with persistent Ed25519 client key + rich DeviceInfo.
	var health ckframework.HealthResponse
	mustJSON(t, http.MethodGet, ts.URL+"/v1/health", nil, nil, http.StatusOK, &health)
	if health.PairingCode == "" || health.Versions.Framework != ckframework.Version {
		t.Fatalf("unexpected health response: %+v", health)
	}

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPubB64 := base64.StdEncoding.EncodeToString(clientPub)
	proof := ed25519.Sign(clientPriv, []byte(health.ServerPub+"pair-nonce-1"))

	var pairResp PairResponse
	mustJSON(t, http.MethodPost, ts.URL+"/v1/pair", nil, PairRequest{
		ClientPub: clientPubB64,
		PairCode:  health.PairingCode,
		Nonce:     "pair-nonce-1",
		Proof:     base64.StdEncoding.EncodeToString(proof),
		DeviceInfo: ckframework.DeviceInfo{
			DeviceName:       "Reg Tablet #1",
			DeviceType:       "android_tablet",
			Manufacturer:     "Samsung",
			Model:            "SM-X710",
			OSName:           "Android",
			OSVersion:        "14",
			AppVersion:       "1.2.0",
			FrameworkVersion: ckframework.Version,
			ProtocolVersion:  ckframework.ProtocolVersion,
		},
	}, http.StatusOK, &pairResp)
	if pairResp.DeviceToken == "" {
		t.Fatalf("expected device_token on pair")
	}

	// 2. Zero-PIN challenge-response reconnect with just IP + client key.
	var chal struct {
		Nonce     string `json:"nonce"`
		ServerPub string `json:"server_pub"`
		ServerSig string `json:"server_sig"`
	}
	mustJSON(t, http.MethodPost, ts.URL+"/v1/auth/challenge", nil, nil, http.StatusOK, &chal)
	srvSigBytes, _ := base64.StdEncoding.DecodeString(chal.ServerSig)
	srvPubBytes, _ := base64.StdEncoding.DecodeString(chal.ServerPub)
	if !ed25519.Verify(ed25519.PublicKey(srvPubBytes), []byte(chal.Nonce), srvSigBytes) {
		t.Fatalf("server challenge signature failed mutual auth")
	}
	clientSig := ed25519.Sign(clientPriv, []byte(chal.Nonce+chal.ServerPub))
	var authResp struct {
		DeviceToken string `json:"device_token"`
	}
	mustJSON(t, http.MethodPost, ts.URL+"/v1/auth/verify", nil, map[string]any{
		"client_id": pairResp.DeviceID,
		"nonce":     chal.Nonce,
		"sig":       base64.StdEncoding.EncodeToString(clientSig),
	}, http.StatusOK, &authResp)

	devHeaders := map[string]string{"X-Device-Token": authResp.DeviceToken}

	// 3. Operator AuthGuard: config rejected without operator JWT, allowed after operator/operator login.
	mustJSON(t, http.MethodGet, ts.URL+"/v1/config", devHeaders, nil, http.StatusUnauthorized, nil)

	var loginResp OperatorLoginResponse
	mustJSON(t, http.MethodPost, ts.URL+"/v1/operators/login", devHeaders, map[string]string{
		"username": "operator",
		"password": "operator",
	}, http.StatusOK, &loginResp)
	if loginResp.AccessToken == "" {
		t.Fatalf("expected operator access token")
	}
	opHeaders := map[string]string{
		"X-Device-Token": authResp.DeviceToken,
		"Authorization":  "Bearer " + loginResp.AccessToken,
	}

	// 4. Connect mock Godot over loopback WS /v1/bridge.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridgeURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/bridge?token=test-bridge-token"
	bridgeConn, _, err := websocket.Dial(ctx, bridgeURL, nil)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer bridgeConn.CloseNow()

	writeBridge(t, ctx, bridgeConn, ckframework.BridgeMessage{
		T:              "ready",
		GameName:       "test-arcade",
		GameVersion:    "2.1.0",
		EngineVersion:  "godot-4.4",
		BridgeProtocol: ckframework.ProtocolVersion,
	})
	hello := readBridge(t, ctx, bridgeConn)
	if hello.T != "hello" {
		t.Fatalf("expected hello from bridge, got %+v", hello)
	}
	_ = readBridge(t, ctx, bridgeConn) // initial board_snapshot

	// 5. Real-time settings patch mirrors to Godot immediately.
	mustJSON(t, http.MethodPatch, ts.URL+"/v1/config", opHeaders, map[string]any{
		"expected_revision": 0,
		"values":            map[string]any{"display.mode": "centered", "debug.hitboxes": true},
	}, http.StatusOK, nil)
	cfgPush := readBridge(t, ctx, bridgeConn)
	if cfgPush.T != "config" || cfgPush.Values["display.mode"] != "centered" || cfgPush.RuntimeValues["debug.hitboxes"] != true {
		t.Fatalf("unexpected bridge config push: %+v", cfgPush)
	}

	// 6. ActionSpec trigger dispatched to Godot over /v1/bridge and returns ActionResult.
	go func() {
		actMsg := readBridge(t, ctx, bridgeConn)
		if actMsg.T == "action" && actMsg.ActionID == "game.seed_demo" {
			writeBridge(t, ctx, bridgeConn, ckframework.BridgeMessage{
				T:        "action_result",
				CallID:   actMsg.CallID,
				ActionID: actMsg.ActionID,
				OK:       true,
				Message:  "Spawned 12 demo items",
			})
		}
	}()
	var actRes ckframework.ActionResult
	mustJSON(t, http.MethodPost, ts.URL+"/v1/actions/game.seed_demo", opHeaders, map[string]any{}, http.StatusOK, &actRes)
	if !actRes.OK || actRes.Message != "Spawned 12 demo items" {
		t.Fatalf("unexpected action result: %+v", actRes)
	}

	// 7. Form submission (CKTelemetryTransmit) + round finish -> populates Leaderboard.
	var roundResp struct {
		SubmissionID string `json:"submission_id"`
		RoundID      string `json:"round_id"`
	}
	mustJSON(t, http.MethodPost, ts.URL+"/v1/rounds", devHeaders, map[string]any{
		"request_id": "req-0001-abc",
		"form_id":    "registration",
		"fields":     map[string]any{"name": "Reagan", "email": "reagan@example.com"},
	}, http.StatusAccepted, &roundResp)
	armMsg := readBridge(t, ctx, bridgeConn)
	if armMsg.T != "arm" || armMsg.RoundID != roundResp.RoundID {
		t.Fatalf("expected arm message, got %+v", armMsg)
	}

	writeBridge(t, ctx, bridgeConn, ckframework.BridgeMessage{
		T:       "finished",
		RoundID: roundResp.RoundID,
		Score:   420,
		Reason:  "completed",
	})
	boardPush := readBridge(t, ctx, bridgeConn)
	if boardPush.T != "board_snapshot" || len(boardPush.Rows) != 1 || boardPush.Rows[0].SortValue != 420 {
		t.Fatalf("expected leaderboard snapshot with score 420, got %+v", boardPush)
	}

	// 8. Delete leaderboard row (no edit allowed) -> tombstones board row, notifies Godot, preserves submission!
	mustJSON(t, http.MethodDelete, ts.URL+"/v1/boards/top_scores/rows/"+roundResp.RoundID, opHeaders, nil, http.StatusOK, nil)
	rmMsg := readBridge(t, ctx, bridgeConn)
	if rmMsg.T != "board_row_removed" || rmMsg.EntryID != roundResp.RoundID {
		t.Fatalf("expected board_row_removed on bridge, got %+v", rmMsg)
	}
	_ = readBridge(t, ctx, bridgeConn) // updated empty board_snapshot

	var subList struct {
		Items []ckframework.SubmissionRecord `json:"items"`
		Total int64                          `json:"total"`
	}
	mustJSON(t, http.MethodGet, ts.URL+"/v1/submissions", opHeaders, nil, http.StatusOK, &subList)
	if subList.Total != 1 || subList.Items[0].ID != roundResp.SubmissionID || subList.Items[0].SourceDevice != "Reg Tablet #1" {
		t.Fatalf("expected immutable submission preserved with device attribution, got %+v", subList)
	}

	// 9. Verify CKTelemetryTransmit wrote form.submission into outbox & Tablet-as-Mule pull/ack works.
	var pullResp struct {
		Items  []cktelemetry.Event `json:"items"`
		MaxSeq int64               `json:"max_seq"`
	}
	mustJSON(t, http.MethodGet, ts.URL+"/v1/telemetry/pull?after_seq=0&limit=50", devHeaders, nil, http.StatusOK, &pullResp)
	foundFormEv := false
	ids := make([]string, 0, len(pullResp.Items))
	for _, ev := range pullResp.Items {
		ids = append(ids, ev.EventID)
		if ev.Kind == "form.submission" && ev.OriginDeviceID == pairResp.DeviceID {
			foundFormEv = true
		}
	}
	if !foundFormEv {
		t.Fatalf("expected form.submission in telemetry outbox, got %d events", len(pullResp.Items))
	}

	// Query Telemetry Explorer joined with devices table.
	explorerRows, err := srv.Outbox().Query(ctx, cktelemetry.QueryFilter{OriginDeviceID: pairResp.DeviceID})
	if err != nil || len(explorerRows) == 0 || explorerRows[0].DeviceName != "Reg Tablet #1" || explorerRows[0].Model != "SM-X710" {
		t.Fatalf("expected telemetry explorer join with Samsung SM-X710, got err=%v rows=%+v", err, explorerRows)
	}

	// ACK pulled events from tablet mule.
	mustJSON(t, http.MethodPost, ts.URL+"/v1/telemetry/ack", devHeaders, map[string]any{
		"synced_event_ids": ids,
		"up_to_seq":        pullResp.MaxSeq,
	}, http.StatusOK, nil)
	pending, _ := srv.Outbox().PendingCount(ctx)
	if pending != 0 {
		t.Fatalf("expected 0 pending after mule ack, got %d", pending)
	}

	// 10. Changing operator password immediately invalidates previously issued JWT.
	mustJSON(t, http.MethodPatch, ts.URL+"/v1/operators/"+loginResp.Operator.ID, opHeaders, map[string]string{
		"password": "new-booth-password",
	}, http.StatusOK, nil)
	mustJSON(t, http.MethodGet, ts.URL+"/v1/config", opHeaders, nil, http.StatusUnauthorized, nil)
}

func mustJSON(t *testing.T, method, url string, headers map[string]string, body any, wantStatus int, out any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(resp.Body)
		t.Fatalf("%s %s: want status %d, got %d: %s", method, url, wantStatus, resp.StatusCode, raw.String())
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, url, err)
		}
	}
}

func writeBridge(t *testing.T, ctx context.Context, conn *websocket.Conn, msg ckframework.BridgeMessage) {
	t.Helper()
	raw, _ := json.Marshal(msg)
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write bridge: %v", err)
	}
}

func readBridge(t *testing.T, ctx context.Context, conn *websocket.Conn) ckframework.BridgeMessage {
	t.Helper()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read bridge: %v", err)
	}
	var msg ckframework.BridgeMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal bridge: %v", err)
	}
	return msg
}
