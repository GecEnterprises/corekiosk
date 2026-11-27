package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
	"github.com/georgysavva/scany/v2/sqlscan"
)

func (s *Store) UpsertDevice(ctx context.Context, d ckframework.DeviceRecord) error {
	infoJSON := "{}"
	if d.DeviceInfo != nil {
		if raw, err := json.Marshal(d.DeviceInfo); err == nil {
			infoJSON = string(raw)
		}
	}
	allowRelay := 0
	if d.AllowCloudRelay {
		allowRelay = 1
	}
	q, args, err := dialect.Insert("devices").
		Rows(goqu.Record{
			"device_id":         d.DeviceID,
			"public_key":        d.PublicKey,
			"role":              d.Role,
			"device_name":       d.DeviceName,
			"device_type":       d.DeviceType,
			"manufacturer":      d.Manufacturer,
			"model":             d.Model,
			"os_name":           d.OSName,
			"os_version":        d.OSVersion,
			"app_version":       d.AppVersion,
			"framework_version": d.FrameworkVersion,
			"launcher_version":  d.LauncherVersion,
			"game_version":      d.GameVersion,
			"protocol_version":  d.ProtocolVersion,
			"device_info_json":  infoJSON,
			"allow_cloud_relay": allowRelay,
			"last_ip":           d.LastIP,
			"paired_at_ms":      d.PairedAtMs,
			"last_seen_at_ms":   d.LastSeenAtMs,
			"revoked_at_ms":     d.RevokedAtMs,
		}).
		OnConflict(goqu.DoUpdate("device_id", goqu.Record{
			"role":              goqu.L("excluded.role"),
			"device_name":       goqu.L("CASE WHEN excluded.device_name != '' THEN excluded.device_name ELSE devices.device_name END"),
			"device_type":       goqu.L("CASE WHEN excluded.device_type != '' THEN excluded.device_type ELSE devices.device_type END"),
			"manufacturer":      goqu.L("CASE WHEN excluded.manufacturer != '' THEN excluded.manufacturer ELSE devices.manufacturer END"),
			"model":             goqu.L("CASE WHEN excluded.model != '' THEN excluded.model ELSE devices.model END"),
			"os_name":           goqu.L("CASE WHEN excluded.os_name != '' THEN excluded.os_name ELSE devices.os_name END"),
			"os_version":        goqu.L("CASE WHEN excluded.os_version != '' THEN excluded.os_version ELSE devices.os_version END"),
			"app_version":       goqu.L("CASE WHEN excluded.app_version != '' THEN excluded.app_version ELSE devices.app_version END"),
			"framework_version": goqu.L("CASE WHEN excluded.framework_version != '' THEN excluded.framework_version ELSE devices.framework_version END"),
			"launcher_version":  goqu.L("CASE WHEN excluded.launcher_version != '' THEN excluded.launcher_version ELSE devices.launcher_version END"),
			"game_version":      goqu.L("CASE WHEN excluded.game_version != '' THEN excluded.game_version ELSE devices.game_version END"),
			"protocol_version":  goqu.L("CASE WHEN excluded.protocol_version > 0 THEN excluded.protocol_version ELSE devices.protocol_version END"),
			"device_info_json":  goqu.L("excluded.device_info_json"),
			"last_ip":           goqu.L("CASE WHEN excluded.last_ip != '' THEN excluded.last_ip ELSE devices.last_ip END"),
			"last_seen_at_ms":   goqu.L("excluded.last_seen_at_ms"),
			"revoked_at_ms":     nil,
		})).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build upsert device")
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return ckframework.ErrStore.Wrap(err, "upsert device")
	}
	return nil
}

type deviceScanRow struct {
	ckframework.DeviceRecord
	DeviceInfoJSON string `db:"device_info_json"`
}

func (r deviceScanRow) toRecord() ckframework.DeviceRecord {
	d := r.DeviceRecord
	if r.DeviceInfoJSON != "" && r.DeviceInfoJSON != "{}" {
		_ = json.Unmarshal([]byte(r.DeviceInfoJSON), &d.DeviceInfo)
	}
	return d
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (*ckframework.DeviceRecord, error) {
	q, args, err := deviceSelect().Where(goqu.C("device_id").Eq(deviceID)).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build get device")
	}
	var row deviceScanRow
	if err := sqlscan.Get(ctx, s.db, &row, q, args...); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "scan device")
	}
	rec := row.toRecord()
	return &rec, nil
}

func (s *Store) ListDevices(ctx context.Context, includeRevoked bool) ([]ckframework.DeviceRecord, error) {
	ds := deviceSelect()
	if !includeRevoked {
		ds = ds.Where(goqu.C("revoked_at_ms").IsNull())
	}
	q, args, err := ds.Order(goqu.C("role").Asc(), goqu.C("paired_at_ms").Asc()).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build list devices")
	}
	var scanned []deviceScanRow
	if err := sqlscan.Select(ctx, s.db, &scanned, q, args...); err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "list devices")
	}
	out := make([]ckframework.DeviceRecord, len(scanned))
	for i, r := range scanned {
		out[i] = r.toRecord()
	}
	return out, nil
}

func (s *Store) TouchDevice(ctx context.Context, deviceID, lastIP string, nowMs int64) error {
	rec := goqu.Record{"last_seen_at_ms": nowMs}
	if lastIP != "" {
		rec["last_ip"] = lastIP
	}
	q, args, err := dialect.Update("devices").Set(rec).Where(goqu.C("device_id").Eq(deviceID)).ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build touch device")
	}
	_, err = s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "touch device")
	}
	return nil
}

func (s *Store) RevokeDevice(ctx context.Context, deviceID string, nowMs int64) error {
	q, args, err := dialect.Update("devices").
		Set(goqu.Record{"revoked_at_ms": nowMs}).
		Where(goqu.C("device_id").Eq(deviceID), goqu.C("role").Neq("server")).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build revoke device")
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "revoke device")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ckframework.ErrNotFound.New("device %q not found or cannot be revoked", deviceID)
	}
	return nil
}

func deviceSelect() *goqu.SelectDataset {
	return dialect.From("devices").Select(
		"device_id", "public_key", "role", "device_name", "device_type",
		"manufacturer", "model", "os_name", "os_version", "app_version",
		"framework_version", "launcher_version", "game_version", "protocol_version",
		"device_info_json", "allow_cloud_relay", "last_ip",
		"paired_at_ms", "last_seen_at_ms", "revoked_at_ms",
	)
}

type rowScanner interface {
	Scan(dest ...any) error
}
