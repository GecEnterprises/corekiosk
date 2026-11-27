package cktelemetry

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"gopkg.in/guregu/null.v4"
)

var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

// NewEventID generates a globally unique, time-ordered ULID used as the
// deterministic idempotency key across local SQLite, LAN relay, and Firebase.
func NewEventID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now().UTC()), ulidEntropy).String()
}

// Event is one immutable record in the local-first telemetry outbox.
type Event struct {
	Seq             int64          `json:"seq,omitempty" db:"seq"`
	EventID         string         `json:"event_id" db:"event_id"`
	OriginDeviceID  string         `json:"origin_device_id" db:"origin_device_id"`
	OriginRole      string         `json:"origin_role" db:"origin_role"` // server | tablet | game
	OriginBootID    string         `json:"origin_boot_id" db:"origin_boot_id"`
	OriginSeq       int64          `json:"origin_seq" db:"origin_seq"`
	Category        string         `json:"category" db:"category"` // wifi | battery | ws | launcher | form | board | audit | system
	Kind            string         `json:"kind" db:"kind"`
	Severity        string         `json:"severity" db:"severity"` // debug | info | warn | error | critical
	OccurredAtMs    int64          `json:"occurred_at_ms" db:"occurred_at_ms"`
	Payload         map[string]any `json:"payload" db:"-"`
	RelayedFrom     string         `json:"relayed_from,omitempty" db:"-"`
	CloudSyncedAtMs null.Int       `json:"cloud_synced_at_ms" db:"cloud_synced_at_ms"`
	SyncAttempts    int            `json:"sync_attempts,omitempty" db:"sync_attempts"`
	LastSyncError   string         `json:"last_sync_error,omitempty" db:"-"`
}

// EventWithDevice enriches an Event with joined hardware metadata from the devices table
// for cklauncher's offline Telemetry Explorer.
type EventWithDevice struct {
	Event
	DeviceName   string `json:"device_name,omitempty" db:"device_name"`
	DeviceType   string `json:"device_type,omitempty" db:"device_type"`
	Manufacturer string `json:"manufacturer,omitempty" db:"manufacturer"`
	Model        string `json:"model,omitempty" db:"model"`
	OSName       string `json:"os_name,omitempty" db:"os_name"`
	OSVersion    string `json:"os_version,omitempty" db:"os_version"`
	AppVersion   string `json:"app_version,omitempty" db:"app_version"`
}

// QueryFilter controls Telemetry Explorer filtering in cklauncher / ckserver.
type QueryFilter struct {
	OriginRole     string
	OriginDeviceID string
	Category       string
	Severity       string
	SyncState      string // all | pending | synced | relayed
	Search         string
	Limit          int
	Offset         int
}

// BatchEnvelope is the signed payload sent to Firebase / cktelemetry-server POST /v1/ingest.
type BatchEnvelope struct {
	InstallationID string  `json:"installation_id"`
	AppName        string  `json:"app_name"`
	SenderDeviceID string  `json:"sender_device_id"`
	SentAtMs       int64   `json:"sent_at_ms"`
	Events         []Event `json:"events"`
}

// IngestResponse is returned by Firebase / cktelemetry-server POST /v1/ingest.
type IngestResponse struct {
	AcceptedIDs []string `json:"accepted_ids"`
	ServerMs    int64    `json:"server_ms"`
}
