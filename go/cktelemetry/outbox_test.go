package cktelemetry

import (
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestOutboxDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE devices (
    device_id TEXT PRIMARY KEY, device_name TEXT, device_type TEXT,
    manufacturer TEXT, model TEXT, os_name TEXT, os_version TEXT, app_version TEXT
);
CREATE TABLE telemetry_outbox (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    origin_device_id TEXT NOT NULL,
    origin_role TEXT NOT NULL,
    origin_boot_id TEXT NOT NULL,
    origin_seq INTEGER NOT NULL,
    category TEXT NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    occurred_at_ms INTEGER NOT NULL,
    payload_json TEXT NOT NULL,
    relayed_from TEXT,
    cloud_synced_at_ms INTEGER,
    sync_attempts INTEGER NOT NULL DEFAULT 0,
    last_sync_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE telemetry_peer_cursors (
    peer_device_id TEXT PRIMARY KEY,
    last_pulled_seq INTEGER NOT NULL DEFAULT 0,
    last_acked_seq INTEGER NOT NULL DEFAULT 0,
    updated_at_ms INTEGER NOT NULL
);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOutboxRelayAndSignedShipper(t *testing.T) {
	db := openTestOutboxDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO devices VALUES ('srv01','Booth PC','windows_pc','Dell','OptiPlex','Windows','11','0.1.0')`)
	_, _ = db.Exec(`INSERT INTO devices VALUES ('tab01','Reg Tablet','android_tablet','Samsung','SM-X710','Android','14','1.0.0')`)

	outbox := NewOutbox(db, "srv01", "server", "boot1")
	ctx := context.Background()

	// Record local server event + ingest relayed tablet Wi-Fi roam event.
	if _, err := outbox.Record(ctx, Event{Category: "launcher", Kind: "launcher.boot", Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	roamID := NewEventID()
	accepted, err := outbox.IngestRelayed(ctx, "tab01", []Event{
		{EventID: roamID, OriginDeviceID: "tab01", OriginRole: "tablet", Category: "wifi", Kind: "net.wifi_roamed", Severity: "warn", Payload: map[string]any{"from_bssid": "aa:bb", "to_bssid": "cc:dd"}},
		{EventID: roamID, OriginDeviceID: "tab01", OriginRole: "tablet", Category: "wifi", Kind: "net.wifi_roamed", Severity: "warn", Payload: map[string]any{"from_bssid": "aa:bb", "to_bssid": "cc:dd"}},
	})
	if err != nil || len(accepted) != 2 {
		t.Fatalf("ingest relayed failed: accepted=%v err=%v", accepted, err)
	}

	// Telemetry Explorer filter by client key & category.
	rows, err := outbox.Query(ctx, QueryFilter{OriginRole: "tablet", OriginDeviceID: "tab01", Category: "wifi"})
	if err != nil || len(rows) != 1 || rows[0].Model != "SM-X710" || rows[0].RelayedFrom != "tab01" {
		t.Fatalf("unexpected explorer rows: err=%v rows=%+v", err, rows)
	}

	// Shipper signs gzipped batch with Ed25519 and verifies on mock Firebase ingest endpoint.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var received int
	mockFB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip reader: %v", err)
			w.WriteHeader(400)
			return
		}
		defer gz.Close()
		raw, _ := io.ReadAll(gz)
		if err := VerifyBatchSignature(pub, raw, r.Header.Get("X-CK-Signature")); err != nil {
			t.Errorf("verify signature: %v", err)
			w.WriteHeader(401)
			return
		}
		var env BatchEnvelope
		_ = json.Unmarshal(raw, &env)
		ids := make([]string, 0, len(env.Events))
		for _, ev := range env.Events {
			ids = append(ids, ev.EventID)
		}
		received += len(ids)
		_ = json.NewEncoder(w).Encode(IngestResponse{AcceptedIDs: ids})
	}))
	defer mockFB.Close()

	shipper := NewShipper(outbox, ShipperConfig{
		IngestURL:      mockFB.URL,
		InstallationID: "srv01",
		AppName:        "test-app",
		DeviceID:       "srv01",
		PrivateKey:     priv,
	})
	synced, err := shipper.FlushNow(ctx)
	if err != nil || synced != 2 || received != 2 || !shipper.CloudOnline() {
		t.Fatalf("flush failed: synced=%d received=%d online=%v err=%v", synced, received, shipper.CloudOnline(), err)
	}
	pending, _ := outbox.PendingCount(ctx)
	if pending != 0 {
		t.Fatalf("expected 0 pending after flush, got %d", pending)
	}
}
