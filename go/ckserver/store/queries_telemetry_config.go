package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
)

// TelemetryConfigRecord stores persisted cloud telemetry shipper parameters.
type TelemetryConfigRecord struct {
	IngestURL        string `json:"ingest_url"`
	APIKey           string `json:"api_key"`
	FlushIntervalSec int    `json:"flush_interval_s"`
	BatchSize        int    `json:"batch_size"`
	UpdatedAtMs      int64  `json:"updated_at_ms"`
}

func (s *Store) LoadTelemetryConfig(ctx context.Context) (TelemetryConfigRecord, bool, error) {
	q, args, err := dialect.From("telemetry_config").
		Select("ingest_url", "api_key", "flush_interval_s", "batch_size", "updated_at_ms").
		Where(goqu.C("id").Eq(1)).
		ToSQL()
	if err != nil {
		return TelemetryConfigRecord{}, false, ckframework.ErrStore.Wrap(err, "build load telemetry config")
	}
	var rec TelemetryConfigRecord
	err = s.db.QueryRowContext(ctx, q, args...).Scan(
		&rec.IngestURL,
		&rec.APIKey,
		&rec.FlushIntervalSec,
		&rec.BatchSize,
		&rec.UpdatedAtMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TelemetryConfigRecord{
			FlushIntervalSec: 15,
			BatchSize:        100,
		}, false, nil
	}
	if err != nil {
		return TelemetryConfigRecord{}, false, ckframework.ErrStore.Wrap(err, "load telemetry config")
	}
	if rec.FlushIntervalSec <= 0 {
		rec.FlushIntervalSec = 15
	}
	if rec.BatchSize <= 0 {
		rec.BatchSize = 100
	}
	return rec, true, nil
}

func (s *Store) SaveTelemetryConfig(ctx context.Context, rec TelemetryConfigRecord, nowMs int64) (TelemetryConfigRecord, error) {
	rec.IngestURL = strings.TrimSpace(rec.IngestURL)
	rec.APIKey = strings.TrimSpace(rec.APIKey)
	if rec.FlushIntervalSec <= 0 {
		rec.FlushIntervalSec = 15
	}
	if rec.FlushIntervalSec > 3600 {
		rec.FlushIntervalSec = 3600
	}
	if rec.BatchSize <= 0 {
		rec.BatchSize = 100
	}
	if rec.BatchSize > 500 {
		rec.BatchSize = 500
	}
	rec.UpdatedAtMs = nowMs

	q, args, err := dialect.Insert("telemetry_config").
		Rows(goqu.Record{
			"id":               1,
			"ingest_url":       rec.IngestURL,
			"api_key":          rec.APIKey,
			"flush_interval_s": rec.FlushIntervalSec,
			"batch_size":       rec.BatchSize,
			"updated_at_ms":    rec.UpdatedAtMs,
		}).
		OnConflict(goqu.DoUpdate("id", goqu.Record{
			"ingest_url":       rec.IngestURL,
			"api_key":          rec.APIKey,
			"flush_interval_s": rec.FlushIntervalSec,
			"batch_size":       rec.BatchSize,
			"updated_at_ms":    rec.UpdatedAtMs,
		})).
		ToSQL()
	if err != nil {
		return rec, ckframework.ErrStore.Wrap(err, "build save telemetry config")
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return rec, ckframework.ErrStore.Wrap(err, "save telemetry config")
	}
	return rec, nil
}
