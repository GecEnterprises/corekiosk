package main

import (
	"compress/gzip"
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/doug-martin/goqu/v9"
	_ "github.com/doug-martin/goqu/v9/dialect/sqlite3"
	"github.com/gin-gonic/gin"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	addr := flag.String("addr", ":8090", "HTTP listen address")
	dbPath := flag.String("db", "./data/telemetry_ingest.db", "SQLite database path")
	apiKey := flag.String("api-key", os.Getenv("CK_TELEMETRY_API_KEY"), "Optional shared API key")
	flag.Parse()

	if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := sql.Open("sqlite", *dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	goose.SetBaseFS(migrationsFS)
	_ = goose.SetDialect("sqlite3")
	if err := goose.Up(db, "migrations"); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	dialect := goqu.Dialect("sqlite3")
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "service": "cktelemetry-server", "server_ms": time.Now().UnixMilli()})
	})

	r.POST("/v1/ingest", func(c *gin.Context) {
		if *apiKey != "" && c.GetHeader("X-CK-API-Key") != *apiKey {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
			return
		}
		var reader io.Reader = c.Request.Body
		if c.GetHeader("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid gzip"})
				return
			}
			defer gz.Close()
			reader = gz
		}
		var env cktelemetry.BatchEnvelope
		if err := json.NewDecoder(io.LimitReader(reader, 8<<20)).Decode(&env); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tx failed"})
			return
		}
		defer tx.Rollback()

		accepted := make([]string, 0, len(env.Events))
		now := time.Now().UnixMilli()
		for _, ev := range env.Events {
			if ev.EventID == "" {
				continue
			}
			payloadRaw, _ := json.Marshal(ev.Payload)
			q, args, err := dialect.Insert("ingested_events").
				Rows(goqu.Record{
					"event_id":         ev.EventID,
					"installation_id":  env.InstallationID,
					"app_name":         env.AppName,
					"origin_device_id": ev.OriginDeviceID,
					"origin_role":      ev.OriginRole,
					"category":         ev.Category,
					"kind":             ev.Kind,
					"severity":         ev.Severity,
					"occurred_at_ms":   ev.OccurredAtMs,
					"received_at_ms":   now,
					"payload_json":     string(payloadRaw),
				}).
				OnConflict(goqu.DoNothing()).
				ToSQL()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "query build failed"})
				return
			}
			if _, err := tx.ExecContext(c.Request.Context(), q, args...); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "insert failed"})
				return
			}
			accepted = append(accepted, ev.EventID)
		}
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
			return
		}
		c.JSON(http.StatusOK, cktelemetry.IngestResponse{
			AcceptedIDs: accepted,
			ServerMs:    now,
		})
	})

	slog.Info("cktelemetry-server listening", "addr", *addr)
	if err := r.Run(*addr); err != nil {
		slog.Error("server exit", "err", err)
		os.Exit(1)
	}
}
