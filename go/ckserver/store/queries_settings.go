package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
)

func (s *Store) LoadSettings(ctx context.Context) (int, map[string]any, error) {
	q, args, err := dialect.From("settings").
		Select("revision", "values_json").
		Where(goqu.C("id").Eq(1)).
		ToSQL()
	if err != nil {
		return 0, nil, ckframework.ErrStore.Wrap(err, "build load settings")
	}
	var rev int
	var raw string
	err = s.db.QueryRowContext(ctx, q, args...).Scan(&rev, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, map[string]any{}, nil
	}
	if err != nil {
		return 0, nil, ckframework.ErrStore.Wrap(err, "load settings")
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return rev, map[string]any{}, ckframework.ErrStore.Wrap(err, "decode stored settings json")
	}
	return rev, out, nil
}

// SaveSettings atomically saves persisted settings with optional optimistic revision checking
// (pass expectedRevision < 0 to skip revision guard for live WS patches).
func (s *Store) SaveSettings(ctx context.Context, expectedRevision int, values map[string]any, nowMs int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "begin settings tx")
	}
	defer tx.Rollback()

	q, args, err := dialect.From("settings").Select("revision").Where(goqu.C("id").Eq(1)).ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build select settings rev")
	}
	var current int
	err = tx.QueryRowContext(ctx, q, args...).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, ckframework.ErrStore.Wrap(err, "query settings rev")
	}
	if expectedRevision >= 0 && expectedRevision != current {
		return current, ckframework.ErrRevisionMismatch.New("settings changed elsewhere (current revision %d)", current)
	}

	raw, err := json.Marshal(values)
	if err != nil {
		return current, ckframework.ErrValidation.Wrap(err, "marshal settings")
	}
	next := current + 1
	upQ, upArgs, err := dialect.Insert("settings").
		Rows(goqu.Record{
			"id":            1,
			"revision":      next,
			"values_json":   string(raw),
			"updated_at_ms": nowMs,
		}).
		OnConflict(goqu.DoUpdate("id", goqu.Record{
			"revision":      next,
			"values_json":   string(raw),
			"updated_at_ms": nowMs,
		})).
		ToSQL()
	if err != nil {
		return current, ckframework.ErrStore.Wrap(err, "build upsert settings")
	}
	if _, err := tx.ExecContext(ctx, upQ, upArgs...); err != nil {
		return current, ckframework.ErrStore.Wrap(err, "upsert settings")
	}
	if err := tx.Commit(); err != nil {
		return current, ckframework.ErrStore.Wrap(err, "commit settings tx")
	}
	return next, nil
}
