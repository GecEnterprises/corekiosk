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

// PutAsset stores an image/media asset idempotently by content-addressed SHA-256 ID.
func (s *Store) PutAsset(ctx context.Context, filename, seedKey string, data []byte, nowMs int64) (ckframework.AssetMeta, error) {
	mime, width, height, err := ckframework.InspectImageBytes(data)
	if err != nil {
		return ckframework.AssetMeta{}, err
	}
	id, shaHex := ckframework.AssetIDFromBytes(data)
	meta := ckframework.AssetMeta{
		ID:          id,
		SHA256:      shaHex,
		Filename:    filename,
		MimeType:    mime,
		Width:       width,
		Height:      height,
		SizeBytes:   int64(len(data)),
		SeedKey:     seedKey,
		CreatedAtMs: nowMs,
	}

	const insertSQL = `INSERT INTO assets (id, sha256, filename, mime_type, width, height, size_bytes, seed_key, data, created_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`
	if _, err := s.db.ExecContext(ctx, insertSQL,
		meta.ID, meta.SHA256, meta.Filename, meta.MimeType, meta.Width, meta.Height, meta.SizeBytes, meta.SeedKey, data, meta.CreatedAtMs,
	); err != nil {
		return ckframework.AssetMeta{}, ckframework.ErrStore.Wrap(err, "insert asset")
	}
	return meta, nil
}

// GetAsset retrieves asset metadata and raw bytes by ID.
func (s *Store) GetAsset(ctx context.Context, id string) (ckframework.AssetMeta, []byte, error) {
	q, args, err := dialect.From("assets").
		Select("id", "sha256", "filename", "mime_type", "width", "height", "size_bytes", "seed_key", "data", "created_at_ms").
		Where(goqu.C("id").Eq(id)).
		ToSQL()
	if err != nil {
		return ckframework.AssetMeta{}, nil, ckframework.ErrStore.Wrap(err, "build get asset")
	}
	var m ckframework.AssetMeta
	var data []byte
	err = s.db.QueryRowContext(ctx, q, args...).Scan(
		&m.ID, &m.SHA256, &m.Filename, &m.MimeType, &m.Width, &m.Height, &m.SizeBytes, &m.SeedKey, &data, &m.CreatedAtMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ckframework.AssetMeta{}, nil, ckframework.ErrNotFound.New("asset %q not found", id)
	}
	if err != nil {
		return ckframework.AssetMeta{}, nil, ckframework.ErrStore.Wrap(err, "get asset")
	}
	return m, data, nil
}

// ListAssets returns stored asset metadata (newest first) without raw blob bytes.
func (s *Store) ListAssets(ctx context.Context, limit int) ([]ckframework.AssetMeta, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q, args, err := dialect.From("assets").
		Select("id", "sha256", "filename", "mime_type", "width", "height", "size_bytes", "seed_key", "created_at_ms").
		Order(goqu.C("created_at_ms").Desc()).
		Limit(uint(limit)).
		ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build list assets")
	}
	out := []ckframework.AssetMeta{}
	if err := sqlscan.Select(ctx, s.db, &out, q, args...); err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "list assets")
	}
	return out, nil
}

// LoadCatalog reads persisted entries for catalogID; returns (0, nil, false, nil) if never saved.
func (s *Store) LoadCatalog(ctx context.Context, catalogID string) (int, []ckframework.CatalogEntry, int64, bool, error) {
	q, args, err := dialect.From("catalogs_state").
		Select("revision", "entries_json", "updated_at_ms").
		Where(goqu.C("catalog_id").Eq(catalogID)).
		ToSQL()
	if err != nil {
		return 0, nil, 0, false, ckframework.ErrStore.Wrap(err, "build load catalog")
	}
	var rev int
	var raw string
	var updatedAt int64
	err = s.db.QueryRowContext(ctx, q, args...).Scan(&rev, &raw, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, 0, false, nil
	}
	if err != nil {
		return 0, nil, 0, false, ckframework.ErrStore.Wrap(err, "load catalog")
	}
	var entries []ckframework.CatalogEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return rev, nil, updatedAt, true, ckframework.ErrStore.Wrap(err, "decode catalog entries")
	}
	return rev, entries, updatedAt, true, nil
}

// SaveCatalog atomically persists a catalog's entries with optional optimistic revision checking.
func (s *Store) SaveCatalog(ctx context.Context, catalogID string, expectedRevision int, entries []ckframework.CatalogEntry, nowMs int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "begin save catalog tx")
	}
	defer tx.Rollback()

	q, args, err := dialect.From("catalogs_state").Select("revision").Where(goqu.C("catalog_id").Eq(catalogID)).ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build query catalog rev")
	}
	var current int
	err = tx.QueryRowContext(ctx, q, args...).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, ckframework.ErrStore.Wrap(err, "query catalog rev")
	}
	if expectedRevision >= 0 && expectedRevision != current {
		return current, ckframework.ErrRevisionMismatch.New("catalog %q changed elsewhere (revision %d)", catalogID, current)
	}

	raw, err := json.Marshal(entries)
	if err != nil {
		return current, ckframework.ErrValidation.Wrap(err, "marshal catalog entries")
	}
	next := current + 1
	upQ, upArgs, err := dialect.Insert("catalogs_state").
		Rows(goqu.Record{
			"catalog_id":    catalogID,
			"revision":      next,
			"entries_json":  string(raw),
			"updated_at_ms": nowMs,
		}).
		OnConflict(goqu.DoUpdate("catalog_id", goqu.Record{
			"revision":      next,
			"entries_json":  string(raw),
			"updated_at_ms": nowMs,
		})).
		ToSQL()
	if err != nil {
		return current, ckframework.ErrStore.Wrap(err, "build upsert catalog")
	}
	if _, err := tx.ExecContext(ctx, upQ, upArgs...); err != nil {
		return current, ckframework.ErrStore.Wrap(err, "upsert catalog")
	}
	if err := tx.Commit(); err != nil {
		return current, ckframework.ErrStore.Wrap(err, "commit catalog tx")
	}
	return next, nil
}

// LoadRuleset reads persisted rules for rulesetID; returns (0, nil, false, nil) if never saved.
func (s *Store) LoadRuleset(ctx context.Context, rulesetID string) (int, []ckframework.RuleEntry, int64, bool, error) {
	q, args, err := dialect.From("rulesets_state").
		Select("revision", "rules_json", "updated_at_ms").
		Where(goqu.C("ruleset_id").Eq(rulesetID)).
		ToSQL()
	if err != nil {
		return 0, nil, 0, false, ckframework.ErrStore.Wrap(err, "build load ruleset")
	}
	var rev int
	var raw string
	var updatedAt int64
	err = s.db.QueryRowContext(ctx, q, args...).Scan(&rev, &raw, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, 0, false, nil
	}
	if err != nil {
		return 0, nil, 0, false, ckframework.ErrStore.Wrap(err, "load ruleset")
	}
	var rules []ckframework.RuleEntry
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return rev, nil, updatedAt, true, ckframework.ErrStore.Wrap(err, "decode ruleset json")
	}
	return rev, rules, updatedAt, true, nil
}

// SaveRuleset atomically persists a ruleset's rules with optional optimistic revision checking.
func (s *Store) SaveRuleset(ctx context.Context, rulesetID string, expectedRevision int, rules []ckframework.RuleEntry, nowMs int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "begin save ruleset tx")
	}
	defer tx.Rollback()

	q, args, err := dialect.From("rulesets_state").Select("revision").Where(goqu.C("ruleset_id").Eq(rulesetID)).ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build query ruleset rev")
	}
	var current int
	err = tx.QueryRowContext(ctx, q, args...).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, ckframework.ErrStore.Wrap(err, "query ruleset rev")
	}
	if expectedRevision >= 0 && expectedRevision != current {
		return current, ckframework.ErrRevisionMismatch.New("ruleset %q changed elsewhere (revision %d)", rulesetID, current)
	}

	raw, err := json.Marshal(rules)
	if err != nil {
		return current, ckframework.ErrValidation.Wrap(err, "marshal ruleset")
	}
	next := current + 1
	upQ, upArgs, err := dialect.Insert("rulesets_state").
		Rows(goqu.Record{
			"ruleset_id":    rulesetID,
			"revision":      next,
			"rules_json":    string(raw),
			"updated_at_ms": nowMs,
		}).
		OnConflict(goqu.DoUpdate("ruleset_id", goqu.Record{
			"revision":      next,
			"rules_json":    string(raw),
			"updated_at_ms": nowMs,
		})).
		ToSQL()
	if err != nil {
		return current, ckframework.ErrStore.Wrap(err, "build upsert ruleset")
	}
	if _, err := tx.ExecContext(ctx, upQ, upArgs...); err != nil {
		return current, ckframework.ErrStore.Wrap(err, "upsert ruleset")
	}
	if err := tx.Commit(); err != nil {
		return current, ckframework.ErrStore.Wrap(err, "commit ruleset tx")
	}
	return next, nil
}
