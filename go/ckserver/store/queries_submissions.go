package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/doug-martin/goqu/v9"
)

// CreateSubmissionWithOutbox atomically inserts an immutable form submission (and optional round)
// plus its CKTelemetryTransmit outbox event in a single SQLite transaction.
func (s *Store) CreateSubmissionWithOutbox(
	ctx context.Context,
	sub ckframework.SubmissionRecord,
	round *RoundRecord,
	outbox *cktelemetry.Outbox,
	telemetryEvent *cktelemetry.Event,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "begin submission tx")
	}
	defer tx.Rollback()

	payloadRaw, err := json.Marshal(sub.Payload)
	if err != nil {
		return ckframework.ErrValidation.Wrap(err, "marshal submission payload")
	}
	outcomeRaw := "{}"
	if sub.Outcome != nil {
		if raw, err := json.Marshal(sub.Outcome); err == nil {
			outcomeRaw = string(raw)
		}
	}
	q, args, err := dialect.Insert("submissions").Rows(goqu.Record{
		"id":               sub.ID,
		"request_id":       sub.RequestID,
		"form_id":          sub.FormID,
		"form_version":     sub.FormVersion,
		"payload_json":     string(payloadRaw),
		"source_device_id": sub.SourceDeviceID,
		"round_id":         sub.RoundID,
		"status":           sub.Status,
		"outcome_json":     outcomeRaw,
		"created_at_ms":    sub.CreatedAtMs,
	}).ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build insert submission")
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return ckframework.ErrConflict.Wrap(err, "insert submission")
	}

	if round != nil {
		rq, rargs, err := dialect.Insert("rounds").Rows(goqu.Record{
			"id":              round.ID,
			"submission_id":   round.SubmissionID,
			"request_id":      round.RequestID,
			"form_id":         round.FormID,
			"state":           round.State,
			"score":           round.Score,
			"finish_reason":   round.FinishReason,
			"config_revision": round.ConfigRevision,
			"created_at_ms":   round.CreatedAtMs,
			"started_at_ms":   round.StartedAtMs,
			"finished_at_ms":  round.FinishedAtMs,
		}).ToSQL()
		if err != nil {
			return ckframework.ErrStore.Wrap(err, "build insert round")
		}
		if _, err := tx.ExecContext(ctx, rq, rargs...); err != nil {
			return ckframework.ErrStore.Wrap(err, "insert round")
		}
	}

	if outbox != nil && telemetryEvent != nil {
		if _, err := outbox.RecordInTx(ctx, tx, *telemetryEvent); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return ckframework.ErrStore.Wrap(err, "commit submission tx")
	}
	return nil
}

// GetSubmissionByRequestID enables idempotent retries across tablet network hiccups.
func (s *Store) GetSubmissionByRequestID(ctx context.Context, requestID string) (*ckframework.SubmissionRecord, error) {
	q, args, err := submissionSelect().Where(goqu.I("s.request_id").Eq(requestID)).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build get submission by request_id")
	}
	rec, err := scanSubmission(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "scan submission")
	}
	return &rec, nil
}

// ListSubmissions returns paginated, searchable read-only form submissions (no delete/edit).
func (s *Store) ListSubmissions(ctx context.Context, formID, search string, limit, offset int) ([]ckframework.SubmissionRecord, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	base := dialect.From(goqu.T("submissions").As("s")).
		LeftJoin(goqu.T("devices").As("d"), goqu.On(goqu.I("s.source_device_id").Eq(goqu.I("d.device_id"))))
	if formID != "" {
		base = base.Where(goqu.I("s.form_id").Eq(formID))
	}
	if qStr := strings.TrimSpace(search); qStr != "" {
		like := "%" + qStr + "%"
		base = base.Where(goqu.Or(
			goqu.I("s.payload_json").ILike(like),
			goqu.I("s.source_device_id").ILike(like),
			goqu.I("d.device_name").ILike(like),
		))
	}

	cq, cargs, err := base.Select(goqu.COUNT("*")).ToSQL()
	if err != nil {
		return nil, 0, ckframework.ErrStore.Wrap(err, "build count submissions")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, cq, cargs...).Scan(&total); err != nil {
		return nil, 0, ckframework.ErrStore.Wrap(err, "count submissions")
	}

	q, args, err := base.Select(
		goqu.I("s.id"), goqu.I("s.request_id"), goqu.I("s.form_id"), goqu.I("s.form_version"),
		goqu.I("s.payload_json"), goqu.I("s.source_device_id"), goqu.COALESCE(goqu.I("d.device_name"), ""),
		goqu.I("s.round_id"), goqu.I("s.status"), goqu.I("s.outcome_json"), goqu.I("s.created_at_ms"),
	).Order(goqu.I("s.created_at_ms").Desc()).Limit(uint(limit)).Offset(uint(max(0, offset))).ToSQL()
	if err != nil {
		return nil, 0, ckframework.ErrStore.Wrap(err, "build list submissions")
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, ckframework.ErrStore.Wrap(err, "list submissions")
	}
	defer rows.Close()

	out := []ckframework.SubmissionRecord{}
	for rows.Next() {
		rec, err := scanSubmission(rows)
		if err != nil {
			return nil, 0, ckframework.ErrStore.Wrap(err, "scan submission row")
		}
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// ExportSubmissionsCSV generates a complete CSV export of immutable historical form rows.
func (s *Store) ExportSubmissionsCSV(ctx context.Context, form ckframework.FormSpec) (string, error) {
	items, _, err := s.ListSubmissions(ctx, form.ID, "", 100000, 0)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := []string{"submission_id", "created_at", "source_device_id", "source_device_name", "status"}
	for _, f := range form.Fields {
		header = append(header, f.Key)
	}
	header = append(header, "score", "reward", "outcome")
	_ = w.Write(header)

	for _, item := range items {
		row := []string{
			item.ID,
			time.UnixMilli(item.CreatedAtMs).UTC().Format(time.RFC3339),
			item.SourceDeviceID,
			item.SourceDevice,
			item.Status,
		}
		for _, f := range form.Fields {
			if v, ok := item.Payload[f.Key]; ok && v != nil {
				row = append(row, fmt.Sprint(v))
			} else {
				row = append(row, "")
			}
		}
		score := ""
		reward := ""
		outcomeStr := ""
		if len(item.Outcome) > 0 {
			if sc, ok := item.Outcome["score"]; ok {
				score = fmt.Sprint(sc)
			}
			if rw, ok := item.Outcome["reward_title"]; ok && rw != nil {
				reward = fmt.Sprint(rw)
			}
			if raw, err := json.Marshal(item.Outcome); err == nil {
				outcomeStr = string(raw)
			}
		}
		row = append(row, score, reward, outcomeStr)
		_ = w.Write(row)
	}
	w.Flush()
	return buf.String(), w.Error()
}

// RoundRecord tracks gameplay round lifecycle linked to an immutable submission.
type RoundRecord struct {
	ID             string
	SubmissionID   string
	RequestID      string
	FormID         string
	State          string
	Score          int
	FinishReason   string
	ConfigRevision int
	CreatedAtMs    int64
	StartedAtMs    int64
	FinishedAtMs   int64
}

func (s *Store) InterruptOpenRounds(ctx context.Context, nowMs int64) error {
	q, args, err := dialect.Update("rounds").
		Set(goqu.Record{
			"state":          "interrupted",
			"finish_reason":  "restart",
			"finished_at_ms": nowMs,
		}).
		Where(goqu.C("state").In("pending", "live")).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build interrupt open rounds")
	}
	_, err = s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "interrupt open rounds")
	}
	return nil
}

func (s *Store) UpdateRoundState(ctx context.Context, roundID, state string, startedAtMs int64) error {
	rec := goqu.Record{"state": state}
	if startedAtMs > 0 {
		rec["started_at_ms"] = startedAtMs
	}
	q, args, err := dialect.Update("rounds").Set(rec).Where(goqu.C("id").Eq(roundID)).ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build update round state")
	}
	_, err = s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "update round state")
	}
	return nil
}

func (s *Store) FinishRound(ctx context.Context, roundID, submissionID string, score int, reason string, nowMs int64) error {
	return s.FinishRoundWithOutcome(ctx, roundID, submissionID, score, reason, nil, nowMs)
}

func (s *Store) FinishRoundWithOutcome(ctx context.Context, roundID, submissionID string, score int, reason string, extraOutcome map[string]any, nowMs int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "begin finish round tx")
	}
	defer tx.Rollback()

	rq, rargs, err := dialect.Update("rounds").
		Set(goqu.Record{
			"state":          "finished",
			"score":          score,
			"finish_reason":  reason,
			"finished_at_ms": nowMs,
		}).
		Where(goqu.C("id").Eq(roundID), goqu.C("state").NotIn("finished", "cancelled", "interrupted")).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build finish round")
	}
	if _, err := tx.ExecContext(ctx, rq, rargs...); err != nil {
		return ckframework.ErrStore.Wrap(err, "finish round")
	}

	outcomeMap := map[string]any{"score": score, "finish_reason": reason, "finished_at_ms": nowMs}
	for k, v := range extraOutcome {
		outcomeMap[k] = v
	}
	outcomeRaw, _ := json.Marshal(outcomeMap)
	sq, sargs, err := dialect.Update("submissions").
		Set(goqu.Record{"status": "played", "outcome_json": string(outcomeRaw)}).
		Where(goqu.C("id").Eq(submissionID)).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build update submission outcome")
	}
	if _, err := tx.ExecContext(ctx, sq, sargs...); err != nil {
		return ckframework.ErrStore.Wrap(err, "update submission outcome")
	}
	if err := tx.Commit(); err != nil {
		return ckframework.ErrStore.Wrap(err, "commit finish round")
	}
	return nil
}

func (s *Store) CancelRound(ctx context.Context, roundID, submissionID, reason string, nowMs int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "begin cancel round tx")
	}
	defer tx.Rollback()

	rq, rargs, err := dialect.Update("rounds").
		Set(goqu.Record{
			"state":          "cancelled",
			"finish_reason":  reason,
			"finished_at_ms": nowMs,
		}).
		Where(goqu.C("id").Eq(roundID), goqu.C("state").NotIn("finished", "cancelled")).
		ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build cancel round")
	}
	_, _ = tx.ExecContext(ctx, rq, rargs...)

	outcomeRaw, _ := json.Marshal(map[string]any{"finish_reason": reason, "cancelled_at_ms": nowMs})
	sq, sargs, err := dialect.Update("submissions").
		Set(goqu.Record{"status": "cancelled", "outcome_json": string(outcomeRaw)}).
		Where(goqu.C("id").Eq(submissionID), goqu.C("status").Neq("played")).
		ToSQL()
	if err == nil {
		_, _ = tx.ExecContext(ctx, sq, sargs...)
	}
	if err := tx.Commit(); err != nil {
		return ckframework.ErrStore.Wrap(err, "commit cancel round")
	}
	return nil
}

func submissionSelect() *goqu.SelectDataset {
	return dialect.From(goqu.T("submissions").As("s")).
		LeftJoin(goqu.T("devices").As("d"), goqu.On(goqu.I("s.source_device_id").Eq(goqu.I("d.device_id")))).
		Select(
			goqu.I("s.id"), goqu.I("s.request_id"), goqu.I("s.form_id"), goqu.I("s.form_version"),
			goqu.I("s.payload_json"), goqu.I("s.source_device_id"), goqu.COALESCE(goqu.I("d.device_name"), ""),
			goqu.I("s.round_id"), goqu.I("s.status"), goqu.I("s.outcome_json"), goqu.I("s.created_at_ms"),
		)
}

func scanSubmission(sc rowScanner) (ckframework.SubmissionRecord, error) {
	var rec ckframework.SubmissionRecord
	var payloadRaw, outcomeRaw string
	if err := sc.Scan(
		&rec.ID, &rec.RequestID, &rec.FormID, &rec.FormVersion,
		&payloadRaw, &rec.SourceDeviceID, &rec.SourceDevice,
		&rec.RoundID, &rec.Status, &outcomeRaw, &rec.CreatedAtMs,
	); err != nil {
		return rec, err
	}
	_ = json.Unmarshal([]byte(payloadRaw), &rec.Payload)
	if outcomeRaw != "" && outcomeRaw != "{}" {
		_ = json.Unmarshal([]byte(outcomeRaw), &rec.Outcome)
	}
	return rec, nil
}
