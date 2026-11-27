package cktelemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
	_ "github.com/doug-martin/goqu/v9/dialect/sqlite3"
	"github.com/georgysavva/scany/v2/sqlscan"
	"gopkg.in/guregu/null.v4"
)

var sqliteDialect = goqu.Dialect("sqlite3")

// Outbox provides local-first SQLite telemetry storage built with goqu.
type Outbox struct {
	db             *sql.DB
	originDeviceID string
	originRole     string
	originBootID   string
	originSeq      atomic.Int64
}

// NewOutbox attaches to an already-migrated SQLite database containing telemetry_outbox.
func NewOutbox(db *sql.DB, originDeviceID, originRole, originBootID string) *Outbox {
	o := &Outbox{
		db:             db,
		originDeviceID: originDeviceID,
		originRole:     originRole,
		originBootID:   originBootID,
	}
	// Resume monotonic origin_seq from existing rows for this boot if any.
	var maxSeq null.Int
	q, args, err := sqliteDialect.From("telemetry_outbox").
		Select(goqu.MAX("origin_seq")).
		Where(goqu.C("origin_device_id").Eq(originDeviceID), goqu.C("origin_boot_id").Eq(originBootID)).
		ToSQL()
	if err == nil {
		if err := db.QueryRow(q, args...).Scan(&maxSeq); err == nil && maxSeq.Valid {
			o.originSeq.Store(maxSeq.Int64)
		}
	}
	return o
}

// Record writes a locally originated event into telemetry_outbox.
func (o *Outbox) Record(ctx context.Context, ev Event) (Event, error) {
	return o.recordWithExec(ctx, o.db, ev)
}

// RecordInTx writes an event inside an existing SQLite transaction (e.g. atomic form submission + outbox).
func (o *Outbox) RecordInTx(ctx context.Context, tx *sql.Tx, ev Event) (Event, error) {
	return o.recordWithExec(ctx, tx, ev)
}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (o *Outbox) recordWithExec(ctx context.Context, exec sqlExecer, ev Event) (Event, error) {
	if ev.EventID == "" {
		ev.EventID = NewEventID()
	}
	if ev.OriginDeviceID == "" {
		ev.OriginDeviceID = o.originDeviceID
	}
	if ev.OriginRole == "" {
		ev.OriginRole = o.originRole
	}
	if ev.OriginBootID == "" {
		ev.OriginBootID = o.originBootID
	}
	if ev.OriginSeq == 0 {
		ev.OriginSeq = o.originSeq.Add(1)
	}
	if ev.Severity == "" {
		ev.Severity = "info"
	}
	if ev.OccurredAtMs == 0 {
		ev.OccurredAtMs = time.Now().UnixMilli()
	}
	if ev.Payload == nil {
		ev.Payload = map[string]any{}
	}
	payloadRaw, err := json.Marshal(ev.Payload)
	if err != nil {
		return ev, ckframework.ErrValidation.Wrap(err, "marshal telemetry payload")
	}

	var relayed any
	if ev.RelayedFrom != "" {
		relayed = ev.RelayedFrom
	}
	q, args, err := sqliteDialect.Insert("telemetry_outbox").
		Rows(goqu.Record{
			"event_id":           ev.EventID,
			"origin_device_id":   ev.OriginDeviceID,
			"origin_role":        ev.OriginRole,
			"origin_boot_id":     ev.OriginBootID,
			"origin_seq":         ev.OriginSeq,
			"category":           ev.Category,
			"kind":               ev.Kind,
			"severity":           ev.Severity,
			"occurred_at_ms":     ev.OccurredAtMs,
			"payload_json":       string(payloadRaw),
			"relayed_from":       relayed,
			"cloud_synced_at_ms": ev.CloudSyncedAtMs,
		}).
		OnConflict(goqu.DoNothing()).
		ToSQL()
	if err != nil {
		return ev, ckframework.ErrStore.Wrap(err, "build telemetry insert")
	}
	res, err := exec.ExecContext(ctx, q, args...)
	if err != nil {
		return ev, ckframework.ErrStore.Wrap(err, "insert telemetry outbox")
	}
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		ev.Seq = id
	}
	return ev, nil
}

// IngestRelayed idempotently stores events received from a paired peer (Tablet -> Server).
func (o *Outbox) IngestRelayed(ctx context.Context, peerDeviceID string, events []Event) ([]string, error) {
	if len(events) == 0 {
		return []string{}, nil
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "begin ingest tx")
	}
	defer tx.Rollback()

	accepted := make([]string, 0, len(events))
	for _, ev := range events {
		if strings.TrimSpace(ev.EventID) == "" || strings.TrimSpace(ev.Kind) == "" {
			continue
		}
		if ev.OriginDeviceID == "" {
			ev.OriginDeviceID = peerDeviceID
		}
		if ev.OriginRole == "" {
			ev.OriginRole = "tablet"
		}
		ev.RelayedFrom = peerDeviceID
		if _, err := o.recordWithExec(ctx, tx, ev); err != nil {
			return nil, err
		}
		accepted = append(accepted, ev.EventID)
	}
	if err := tx.Commit(); err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "commit ingest tx")
	}
	return accepted, nil
}

// PullUnsynced returns unsynced events after a local seq cursor for Tablet-as-Mule or direct shipper.
func (o *Outbox) PullUnsynced(ctx context.Context, afterSeq int64, limit int) ([]Event, int64, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q, args, err := sqliteDialect.From("telemetry_outbox").
		Select(
			"seq", "event_id", "origin_device_id", "origin_role", "origin_boot_id",
			"origin_seq", "category", "kind", "severity", "occurred_at_ms",
			"payload_json", "relayed_from", "cloud_synced_at_ms", "sync_attempts", "last_sync_error",
		).
		Where(
			goqu.C("cloud_synced_at_ms").IsNull(),
			goqu.C("seq").Gt(afterSeq),
		).
		Order(goqu.C("seq").Asc()).
		Limit(uint(limit + 1)).
		ToSQL()
	if err != nil {
		return nil, afterSeq, false, ckframework.ErrStore.Wrap(err, "build pull query")
	}
	var scanned []eventScanRow
	if err := sqlscan.Select(ctx, o.db, &scanned, q, args...); err != nil {
		return nil, afterSeq, false, ckframework.ErrStore.Wrap(err, "query unsynced outbox")
	}
	hasMore := len(scanned) > limit
	if hasMore {
		scanned = scanned[:limit]
	}
	out := make([]Event, len(scanned))
	maxSeq := afterSeq
	for i, r := range scanned {
		ev := r.toEvent()
		out[i] = ev
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	return out, maxSeq, hasMore, nil
}

// MarkCloudSynced marks the given event IDs as durably persisted in Firebase.
func (o *Outbox) MarkCloudSynced(ctx context.Context, eventIDs []string, syncedAtMs int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	if syncedAtMs == 0 {
		syncedAtMs = time.Now().UnixMilli()
	}
	q, args, err := sqliteDialect.Update("telemetry_outbox").
		Set(goqu.Record{
			"cloud_synced_at_ms": syncedAtMs,
			"last_sync_error":    "",
		}).
		Where(goqu.C("event_id").In(eventIDs)).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build mark synced")
	}
	if _, err := o.db.ExecContext(ctx, q, args...); err != nil {
		return ckframework.ErrStore.Wrap(err, "mark synced")
	}
	return nil
}

// RecordSyncFailure increments sync_attempts and saves the last error for diagnostics.
func (o *Outbox) RecordSyncFailure(ctx context.Context, eventIDs []string, errMsg string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	if len(errMsg) > 512 {
		errMsg = errMsg[:512]
	}
	q, args, err := sqliteDialect.Update("telemetry_outbox").
		Set(goqu.Record{
			"sync_attempts":   goqu.L("sync_attempts + 1"),
			"last_sync_error": errMsg,
		}).
		Where(goqu.C("event_id").In(eventIDs)).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build sync failure update")
	}
	_, err = o.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "update sync failure")
	}
	return nil
}

// UpdatePeerCursor records pull/ack progress for a peer tablet.
func (o *Outbox) UpdatePeerCursor(ctx context.Context, peerDeviceID string, pulledSeq, ackedSeq int64) error {
	now := time.Now().UnixMilli()
	q, args, err := sqliteDialect.Insert("telemetry_peer_cursors").
		Rows(goqu.Record{
			"peer_device_id":  peerDeviceID,
			"last_pulled_seq": pulledSeq,
			"last_acked_seq":  ackedSeq,
			"updated_at_ms":   now,
		}).
		OnConflict(goqu.DoUpdate("peer_device_id", goqu.Record{
			"last_pulled_seq": goqu.L("MAX(last_pulled_seq, excluded.last_pulled_seq)"),
			"last_acked_seq":  goqu.L("MAX(last_acked_seq, excluded.last_acked_seq)"),
			"updated_at_ms":   now,
		})).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build peer cursor upsert")
	}
	_, err = o.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "upsert peer cursor")
	}
	return nil
}

// PendingCount returns how many events have not yet been confirmed by Firebase.
func (o *Outbox) PendingCount(ctx context.Context) (int64, error) {
	q, args, err := sqliteDialect.From("telemetry_outbox").
		Select(goqu.COUNT("*")).
		Where(goqu.C("cloud_synced_at_ms").IsNull()).
		ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build pending count")
	}
	var n int64
	if err := o.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "scan pending count")
	}
	return n, nil
}

// Query returns events joined with devices metadata for cklauncher's Telemetry Explorer.
func (o *Outbox) Query(ctx context.Context, f QueryFilter) ([]EventWithDevice, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ds := sqliteDialect.From(goqu.T("telemetry_outbox").As("o")).
		LeftJoin(
			goqu.T("devices").As("d"),
			goqu.On(goqu.I("o.origin_device_id").Eq(goqu.I("d.device_id"))),
		).
		Select(
			goqu.I("o.seq"),
			goqu.I("o.event_id"),
			goqu.I("o.origin_device_id"),
			goqu.I("o.origin_role"),
			goqu.I("o.origin_boot_id"),
			goqu.I("o.origin_seq"),
			goqu.I("o.category"),
			goqu.I("o.kind"),
			goqu.I("o.severity"),
			goqu.I("o.occurred_at_ms"),
			goqu.I("o.payload_json"),
			goqu.I("o.relayed_from"),
			goqu.I("o.cloud_synced_at_ms"),
			goqu.I("o.sync_attempts"),
			goqu.I("o.last_sync_error"),
			goqu.COALESCE(goqu.I("d.device_name"), "").As("device_name"),
			goqu.COALESCE(goqu.I("d.device_type"), "").As("device_type"),
			goqu.COALESCE(goqu.I("d.manufacturer"), "").As("manufacturer"),
			goqu.COALESCE(goqu.I("d.model"), "").As("model"),
			goqu.COALESCE(goqu.I("d.os_name"), "").As("os_name"),
			goqu.COALESCE(goqu.I("d.os_version"), "").As("os_version"),
			goqu.COALESCE(goqu.I("d.app_version"), "").As("app_version"),
		)

	if f.OriginRole != "" && f.OriginRole != "all" {
		ds = ds.Where(goqu.I("o.origin_role").Eq(f.OriginRole))
	}
	if f.OriginDeviceID != "" {
		ds = ds.Where(goqu.I("o.origin_device_id").Eq(f.OriginDeviceID))
	}
	if f.Category != "" && f.Category != "all" {
		ds = ds.Where(goqu.I("o.category").Eq(f.Category))
	}
	if f.Severity != "" && f.Severity != "all" {
		ds = ds.Where(goqu.I("o.severity").Eq(f.Severity))
	}
	switch f.SyncState {
	case "pending":
		ds = ds.Where(goqu.I("o.cloud_synced_at_ms").IsNull())
	case "synced":
		ds = ds.Where(goqu.I("o.cloud_synced_at_ms").IsNotNull())
	case "relayed":
		ds = ds.Where(goqu.I("o.relayed_from").IsNotNull())
	}
	if qStr := strings.TrimSpace(f.Search); qStr != "" {
		like := "%" + qStr + "%"
		ds = ds.Where(goqu.Or(
			goqu.I("o.kind").ILike(like),
			goqu.I("o.payload_json").ILike(like),
			goqu.I("o.origin_device_id").ILike(like),
			goqu.I("d.device_name").ILike(like),
		))
	}

	q, args, err := ds.Order(goqu.I("o.seq").Desc()).Limit(uint(limit)).Offset(uint(max(0, f.Offset))).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build explorer query")
	}
	var scanned []eventWithDeviceScanRow
	if err := sqlscan.Select(ctx, o.db, &scanned, q, args...); err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "run explorer query")
	}
	out := make([]EventWithDevice, len(scanned))
	for i, r := range scanned {
		out[i] = r.toEventWithDevice()
	}
	return out, nil
}

// PruneSynced deletes already-synced rows older than cutoffMs while preserving unsynced rows.
func (o *Outbox) PruneSynced(ctx context.Context, cutoffMs int64) (int64, error) {
	q, args, err := sqliteDialect.Delete("telemetry_outbox").
		Where(
			goqu.C("cloud_synced_at_ms").IsNotNull(),
			goqu.C("cloud_synced_at_ms").Lt(cutoffMs),
		).
		ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build prune query")
	}
	res, err := o.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "prune telemetry outbox")
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type eventScanRow struct {
	Event
	PayloadJSON   string      `db:"payload_json"`
	RelayedCol    null.String `db:"relayed_from"`
	LastSyncErrDB null.String `db:"last_sync_error"`
}

type eventWithDeviceScanRow struct {
	EventWithDevice
	PayloadJSON   string      `db:"payload_json"`
	RelayedCol    null.String `db:"relayed_from"`
	LastSyncErrDB null.String `db:"last_sync_error"`
}

func (r eventScanRow) toEvent() Event {
	ev := r.Event
	_ = json.Unmarshal([]byte(r.PayloadJSON), &ev.Payload)
	ev.RelayedFrom = r.RelayedCol.String
	ev.LastSyncError = r.LastSyncErrDB.String
	return ev
}

func (r eventWithDeviceScanRow) toEventWithDevice() EventWithDevice {
	item := r.EventWithDevice
	_ = json.Unmarshal([]byte(r.PayloadJSON), &item.Payload)
	item.RelayedFrom = r.RelayedCol.String
	item.LastSyncError = r.LastSyncErrDB.String
	return item
}
