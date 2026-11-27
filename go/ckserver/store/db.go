package store

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/doug-martin/goqu/v9"
	_ "github.com/doug-martin/goqu/v9/dialect/sqlite3"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var dialect = goqu.Dialect("sqlite3")

// Store wraps SQLite with goose migrations and goqu query building.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database in WAL mode and runs embedded goose migrations.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, ckframework.ErrStore.Wrap(err, "create database directory")
			}
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, ckframework.ErrStore.Wrap(err, "open sqlite database")
	}
	db.SetMaxOpenConns(1) // Serializes writes cleanly on SQLite WAL while allowing fast reads.
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, ckframework.ErrStore.Wrap(err, "ping sqlite database")
	}

	migFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		_ = db.Close()
		return nil, ckframework.ErrStore.Wrap(err, "open embedded migrations fs")
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migFS)
	if err != nil {
		_ = db.Close()
		return nil, ckframework.ErrStore.Wrap(err, "create goose migration provider")
	}
	if _, err := provider.Up(context.Background()); err != nil {
		_ = db.Close()
		return nil, ckframework.ErrStore.Wrap(err, "apply goose migrations")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

// BackupTo writes a consistent point-in-time SQLite snapshot via VACUUM INTO.
func (s *Store) BackupTo(ctx context.Context, dest string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return ckframework.ErrStore.Wrap(err, "vacuum backup")
	}
	return nil
}
