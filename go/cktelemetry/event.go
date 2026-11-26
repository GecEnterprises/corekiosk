package cktelemetry

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
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
	Seq             int64          `json:"seq,omitempty"`
	EventID         string         `json:"event_id"`
	OriginDeviceID  string         `json:"origin_device_id"`
	OriginRole      string         `json:"origin_role"` // server | tablet | game
	OriginBootID    string         `json:"origin_boot_id"`
	OriginSeq       int64          `json:"origin_seq"`
	Category        string         `json:"category"` // wifi | battery | ws | launcher | form | board | audit | system
	Kind            string         `json:"kind"`
	Severity        string         `json:"severity"` // debug | info | warn | error | critical
	OccurredAtMs    int64          `json:"occurred_at_ms"`
	Payload         map[string]any `json:"payload"`
	RelayedFrom     string         `json:"relayed_from,omitempty"`
	CloudSyncedAtMs *int64         `json:"cloud_synced_at_ms,omitempty"`
	SyncAttempts    int            `json:"sync_attempts,omitempty"`
	LastSyncError   string         `json:"last_sync_error,omitempty"`
}

// EventWithDevice enriches an Event with joined hardware metadata from the devices table
// for cklauncher's offline Telemetry Explorer.
type EventWithDevice struct {
	Event
	DeviceName   string `json:"device_name,omitempty"`
	DeviceType   string `json:"device_type,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	OSName       string `json:"os_name,omitempty"`
	OSVersion    string `json:"os_version,omitempty"`
	AppVersion   string `json:"app_version,omitempty"`
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
