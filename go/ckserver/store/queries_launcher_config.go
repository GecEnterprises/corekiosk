package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
)

// LauncherConfigRecord stores persisted cklauncher startup and supervisor preferences.
type LauncherConfigRecord struct {
	AutoLaunch        bool  `json:"auto_launch"`
	CountdownSeconds  int   `json:"countdown_seconds"`
	AutoRestartOnExit bool  `json:"auto_restart_on_exit"`
	OpenCompanion     bool  `json:"open_companion"`
	UpdatedAtMs       int64 `json:"updated_at_ms"`
}

func (s *Store) LoadLauncherConfig(ctx context.Context) (LauncherConfigRecord, bool, error) {
	q, args, err := dialect.From("launcher_config").
		Select("auto_launch", "countdown_seconds", "auto_restart_on_exit", "open_companion", "updated_at_ms").
		Where(goqu.C("id").Eq(1)).
		ToSQL()
	if err != nil {
		return LauncherConfigRecord{}, false, ckframework.ErrStore.Wrap(err, "build load launcher config")
	}
	var autoLaunch, autoRestart, openComp int
	var rec LauncherConfigRecord
	err = s.db.QueryRowContext(ctx, q, args...).Scan(
		&autoLaunch,
		&rec.CountdownSeconds,
		&autoRestart,
		&openComp,
		&rec.UpdatedAtMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LauncherConfigRecord{
			AutoLaunch:        true,
			CountdownSeconds:  30,
			AutoRestartOnExit: true,
			OpenCompanion:     false,
		}, false, nil
	}
	if err != nil {
		return LauncherConfigRecord{}, false, ckframework.ErrStore.Wrap(err, "load launcher config")
	}
	rec.AutoLaunch = autoLaunch != 0
	rec.AutoRestartOnExit = autoRestart != 0
	rec.OpenCompanion = openComp != 0
	if rec.CountdownSeconds < 0 {
		rec.CountdownSeconds = 0
	}
	return rec, true, nil
}

func (s *Store) SaveLauncherConfig(ctx context.Context, rec LauncherConfigRecord, nowMs int64) (LauncherConfigRecord, error) {
	if rec.CountdownSeconds < 0 {
		rec.CountdownSeconds = 0
	}
	if rec.CountdownSeconds > 3600 {
		rec.CountdownSeconds = 3600
	}
	rec.UpdatedAtMs = nowMs

	q, args, err := dialect.Insert("launcher_config").
		Rows(goqu.Record{
			"id":                   1,
			"auto_launch":          boolToInt(rec.AutoLaunch),
			"countdown_seconds":    rec.CountdownSeconds,
			"auto_restart_on_exit": boolToInt(rec.AutoRestartOnExit),
			"open_companion":       boolToInt(rec.OpenCompanion),
			"updated_at_ms":        rec.UpdatedAtMs,
		}).
		OnConflict(goqu.DoUpdate("id", goqu.Record{
			"auto_launch":          boolToInt(rec.AutoLaunch),
			"countdown_seconds":    rec.CountdownSeconds,
			"auto_restart_on_exit": boolToInt(rec.AutoRestartOnExit),
			"open_companion":       boolToInt(rec.OpenCompanion),
			"updated_at_ms":        rec.UpdatedAtMs,
		})).
		ToSQL()
	if err != nil {
		return rec, ckframework.ErrStore.Wrap(err, "build save launcher config")
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return rec, ckframework.ErrStore.Wrap(err, "save launcher config")
	}
	return rec, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
