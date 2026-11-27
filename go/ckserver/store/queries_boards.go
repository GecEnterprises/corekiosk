package store

import (
	"context"
	"encoding/json"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
	"gopkg.in/guregu/null.v4"
)

// UpsertBoardEntry adds an entry to a server- or game-defined board and applies FIFO eviction if configured.
func (s *Store) UpsertBoardEntry(ctx context.Context, spec ckframework.BoardSpec, entryID, submissionID string, sortValue float64, display map[string]any, nowMs int64) error {
	displayRaw, err := json.Marshal(display)
	if err != nil {
		return ckframework.ErrValidation.Wrap(err, "marshal board display")
	}
	var subVal any
	if submissionID != "" {
		subVal = submissionID
	}
	if spec.DedupeField != "" {
		if rawKey, exists := display[spec.DedupeField]; exists && rawKey != nil && rawKey != "" {
			var existingEntryID string
			var existingSort float64
			dedupeQ, dedupeArgs, derr := dialect.From("board_entries").
				Select("entry_id", "sort_value").
				Where(
					goqu.C("board_id").Eq(spec.ID),
					goqu.C("removed_at_ms").IsNull(),
					goqu.L("json_extract(display_json, ?) = ?", "$."+spec.DedupeField, rawKey),
				).
				Limit(1).
				ToSQL()
			if derr == nil {
				if err := s.db.QueryRowContext(ctx, dedupeQ, dedupeArgs...).Scan(&existingEntryID, &existingSort); err == nil {
					isBetter := sortValue > existingSort
					if spec.SortDir == ckframework.SortAsc {
						isBetter = sortValue < existingSort
					}
					if !isBetter {
						return nil
					}
					_ = s.RemoveBoardEntry(ctx, spec.ID, existingEntryID, "dedupe", nowMs)
				}
			}
		}
	}

	q, args, err := dialect.Insert("board_entries").
		Rows(goqu.Record{
			"board_id":      spec.ID,
			"entry_id":      entryID,
			"submission_id": subVal,
			"sort_value":    sortValue,
			"display_json":  string(displayRaw),
			"created_at_ms": nowMs,
		}).
		OnConflict(goqu.DoNothing()).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build insert board entry")
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return ckframework.ErrStore.Wrap(err, "insert board entry")
	}

	if spec.Eviction == ckframework.EvictionFIFO && spec.MaxRows > 0 {
		// Tombstone oldest rows exceeding MaxRows (keeping full audit & submissions intact).
		subQ, subArgs, err := dialect.From("board_entries").
			Select("entry_id").
			Where(goqu.C("board_id").Eq(spec.ID), goqu.C("removed_at_ms").IsNull()).
			Order(goqu.C("created_at_ms").Desc()).
			Limit(uint(spec.MaxRows)).
			ToSQL()
		if err == nil {
			evictSQL := "UPDATE board_entries SET removed_at_ms = ?, removed_by_operator_id = 'fifo' WHERE board_id = ? AND removed_at_ms IS NULL AND entry_id NOT IN (" + subQ + ")"
			evictArgs := append([]any{nowMs, spec.ID}, subArgs...)
			_, _ = s.db.ExecContext(ctx, evictSQL, evictArgs...)
		}
	}
	return nil
}

// RemoveBoardEntry soft-tombstones a board row without modifying its contents or touching submissions.
func (s *Store) RemoveBoardEntry(ctx context.Context, boardID, entryID, operatorID string, nowMs int64) error {
	q, args, err := dialect.Update("board_entries").
		Set(goqu.Record{
			"removed_at_ms":          nowMs,
			"removed_by_operator_id": operatorID,
		}).
		Where(
			goqu.C("board_id").Eq(boardID),
			goqu.C("entry_id").Eq(entryID),
			goqu.C("removed_at_ms").IsNull(),
		).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build remove board entry")
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "remove board entry")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ckframework.ErrNotFound.New("board row %q not found on %q", entryID, boardID)
	}
	return nil
}

// ClearBoard soft-tombstones all active rows on a board while preserving historical submissions.
func (s *Store) ClearBoard(ctx context.Context, boardID, operatorID string, nowMs int64) (int64, error) {
	q, args, err := dialect.Update("board_entries").
		Set(goqu.Record{
			"removed_at_ms":          nowMs,
			"removed_by_operator_id": operatorID,
		}).
		Where(goqu.C("board_id").Eq(boardID), goqu.C("removed_at_ms").IsNull()).
		ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build clear board")
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "clear board")
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// QueryBoard returns the active ranked rows for a BoardSpec.
func (s *Store) QueryBoard(ctx context.Context, spec ckframework.BoardSpec, limit int) ([]ckframework.BoardRow, error) {
	if limit <= 0 {
		limit = spec.Limit
	}
	if limit <= 0 {
		limit = 20
	}
	ds := dialect.From("board_entries").
		Select("entry_id", "submission_id", "sort_value", "display_json", "created_at_ms").
		Where(goqu.C("board_id").Eq(spec.ID), goqu.C("removed_at_ms").IsNull())

	if spec.MinScore.Valid {
		ds = ds.Where(goqu.C("sort_value").Gte(spec.MinScore.Float64))
	}
	if spec.SortDir == ckframework.SortAsc {
		ds = ds.Order(goqu.C("sort_value").Asc(), goqu.C("created_at_ms").Asc())
	} else {
		ds = ds.Order(goqu.C("sort_value").Desc(), goqu.C("created_at_ms").Asc())
	}

	q, args, err := ds.Limit(uint(limit)).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build query board")
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "query board")
	}
	defer rows.Close()

	out := []ckframework.BoardRow{}
	for rank := 1; rows.Next(); rank++ {
		var r ckframework.BoardRow
		var subID null.String
		var displayRaw string
		if err := rows.Scan(&r.EntryID, &subID, &r.SortValue, &displayRaw, &r.CreatedAtMs); err != nil {
			return nil, ckframework.ErrStore.Wrap(err, "scan board row")
		}
		r.SubmissionID = subID.String
		_ = json.Unmarshal([]byte(displayRaw), &r.Display)
		r.Rank = rank
		out = append(out, r)
	}
	return out, rows.Err()
}
