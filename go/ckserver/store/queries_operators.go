package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
)

// OperatorRecord represents one booth operator account.
type OperatorRecord struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	IsDefault    bool   `json:"is_default"`
	TokenVersion int    `json:"token_version"`
	CreatedAtMs  int64  `json:"created_at_ms"`
	UpdatedAtMs  int64  `json:"updated_at_ms"`
}

func (s *Store) CountOperators(ctx context.Context) (int, error) {
	q, args, err := dialect.From("operators").Select(goqu.COUNT("*")).ToSQL()
	if err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "build count operators")
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, ckframework.ErrStore.Wrap(err, "count operators")
	}
	return n, nil
}

func (s *Store) CreateOperator(ctx context.Context, op OperatorRecord) error {
	isDef := 0
	if op.IsDefault {
		isDef = 1
	}
	q, args, err := dialect.Insert("operators").Rows(goqu.Record{
		"id":            op.ID,
		"username":      op.Username,
		"password_hash": op.PasswordHash,
		"is_default":    isDef,
		"token_version": op.TokenVersion,
		"created_at_ms": op.CreatedAtMs,
		"updated_at_ms": op.UpdatedAtMs,
	}).ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build insert operator")
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return ckframework.ErrConflict.Wrap(err, "create operator %q", op.Username)
	}
	return nil
}

func (s *Store) GetOperatorByUsername(ctx context.Context, username string) (*OperatorRecord, error) {
	q, args, err := operatorSelect().Where(goqu.C("username").Eq(username)).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build get operator by username")
	}
	rec, err := scanOperator(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "scan operator")
	}
	return &rec, nil
}

func (s *Store) GetOperatorByID(ctx context.Context, id string) (*OperatorRecord, error) {
	q, args, err := operatorSelect().Where(goqu.C("id").Eq(id)).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build get operator by id")
	}
	rec, err := scanOperator(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "scan operator")
	}
	return &rec, nil
}

func (s *Store) ListOperators(ctx context.Context) ([]OperatorRecord, error) {
	q, args, err := operatorSelect().Order(goqu.C("created_at_ms").Asc()).ToSQL()
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "build list operators")
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "list operators")
	}
	defer rows.Close()

	out := []OperatorRecord{}
	for rows.Next() {
		rec, err := scanOperator(rows)
		if err != nil {
			return nil, ckframework.ErrStore.Wrap(err, "scan operator row")
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// UpdateOperatorCredentials updates username and/or password hash, incrementing token_version
// so any previously issued Operator JWTs are immediately invalidated.
func (s *Store) UpdateOperatorCredentials(ctx context.Context, id, newUsername, newPasswordHash string, isDefault bool, nowMs int64) error {
	isDef := 0
	if isDefault {
		isDef = 1
	}
	rec := goqu.Record{
		"is_default":    isDef,
		"token_version": goqu.L("token_version + 1"),
		"updated_at_ms": nowMs,
	}
	if newUsername != "" {
		rec["username"] = newUsername
	}
	if newPasswordHash != "" {
		rec["password_hash"] = newPasswordHash
	}
	q, args, err := dialect.Update("operators").Set(rec).Where(goqu.C("id").Eq(id)).ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build update operator")
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrConflict.Wrap(err, "update operator credentials")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ckframework.ErrNotFound.New("operator %q not found", id)
	}
	return nil
}

func (s *Store) DeleteOperator(ctx context.Context, id string) error {
	count, err := s.CountOperators(ctx)
	if err != nil {
		return err
	}
	if count <= 1 {
		return ckframework.ErrConflict.New("cannot delete the last remaining operator account")
	}
	q, args, err := dialect.Delete("operators").Where(goqu.C("id").Eq(id)).ToSQL()
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "build delete operator")
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return ckframework.ErrStore.Wrap(err, "delete operator")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ckframework.ErrNotFound.New("operator %q not found", id)
	}
	return nil
}

func operatorSelect() *goqu.SelectDataset {
	return dialect.From("operators").Select(
		"id", "username", "password_hash", "is_default", "token_version", "created_at_ms", "updated_at_ms",
	)
}

func scanOperator(sc rowScanner) (OperatorRecord, error) {
	var op OperatorRecord
	var isDef int
	if err := sc.Scan(&op.ID, &op.Username, &op.PasswordHash, &isDef, &op.TokenVersion, &op.CreatedAtMs, &op.UpdatedAtMs); err != nil {
		return op, err
	}
	op.IsDefault = isDef != 0
	return op, nil
}
