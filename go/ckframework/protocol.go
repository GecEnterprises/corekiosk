package ckframework

import "gopkg.in/guregu/null.v4"

// WifiInfo reports the active Wi-Fi network for a kiosk server or tablet.
type WifiInfo struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	BSSID     string `json:"bssid,omitempty"`
	SignalPct int    `json:"signal_pct,omitempty"`
	RadioType string `json:"radio_type,omitempty"`
}

// DeviceInfo is sent by clients on pairing and reconnect to populate the devices registry.
type DeviceInfo struct {
	DeviceName       string         `json:"device_name"`
	DeviceType       string         `json:"device_type"` // android_tablet | ipad | android_phone | windows_pc | web
	Manufacturer     string         `json:"manufacturer,omitempty"`
	Model            string         `json:"model,omitempty"`
	OSName           string         `json:"os_name,omitempty"`
	OSVersion        string         `json:"os_version,omitempty"`
	AppVersion       string         `json:"app_version,omitempty"`
	FrameworkVersion string         `json:"framework_version,omitempty"`
	ProtocolVersion  int            `json:"protocol_version,omitempty"`
	Wifi             *WifiInfo      `json:"wifi,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

// DeviceRecord is the persisted hardware + key identity stored in SQLite `devices`.
type DeviceRecord struct {
	DeviceID         string         `json:"device_id" db:"device_id"`
	PublicKey        string         `json:"public_key" db:"public_key"`
	Role             string         `json:"role" db:"role"` // server | tablet | operator_tablet
	DeviceName       string         `json:"device_name" db:"device_name"`
	DeviceType       string         `json:"device_type" db:"device_type"`
	Manufacturer     string         `json:"manufacturer,omitempty" db:"manufacturer"`
	Model            string         `json:"model,omitempty" db:"model"`
	OSName           string         `json:"os_name,omitempty" db:"os_name"`
	OSVersion        string         `json:"os_version,omitempty" db:"os_version"`
	AppVersion       string         `json:"app_version,omitempty" db:"app_version"`
	FrameworkVersion string         `json:"framework_version,omitempty" db:"framework_version"`
	LauncherVersion  string         `json:"launcher_version,omitempty" db:"launcher_version"`
	GameVersion      string         `json:"game_version,omitempty" db:"game_version"`
	ProtocolVersion  int            `json:"protocol_version" db:"protocol_version"`
	DeviceInfo       map[string]any `json:"device_info,omitempty" db:"-"`
	AllowCloudRelay  bool           `json:"allow_cloud_relay" db:"allow_cloud_relay"`
	LastIP           string         `json:"last_ip,omitempty" db:"last_ip"`
	PairedAtMs       int64          `json:"paired_at_ms" db:"paired_at_ms"`
	LastSeenAtMs     int64          `json:"last_seen_at_ms" db:"last_seen_at_ms"`
	RevokedAtMs      null.Int       `json:"revoked_at_ms" db:"revoked_at_ms"`
}

// HealthResponse is returned by unauthenticated GET /v1/health.
type HealthResponse struct {
	Service          string       `json:"service"`
	AppName          string       `json:"app_name"`
	Versions         VersionsInfo `json:"versions"`
	MinClientVersion string       `json:"min_client_version,omitempty"`
	ServerID         string       `json:"server_id"`
	ServerPub        string       `json:"server_pub"`
	BootID           string       `json:"boot_id"`
	Phase            string       `json:"phase"`
	SchemaHash       string       `json:"schema_hash"`
	DisplayOnline    bool         `json:"display_online"`
	ClientsOnline    int          `json:"clients_online"`
	CloudOnline      bool         `json:"cloud_online"`
	Wifi             *WifiInfo    `json:"wifi,omitempty"`
	PairingMode      string       `json:"pairing_mode"`
	ServerMs         int64        `json:"server_ms"`
	PairingCode      string       `json:"pairing_code,omitempty"` // Only populated in dev mode
}

// ActiveRound tracks an in-flight round in Snapshot.
type ActiveRound struct {
	ID           string                   `json:"id"`
	SubmissionID string                   `json:"submission_id"`
	RequestID    string                   `json:"request_id"`
	FormID       string                   `json:"form_id"`
	Fields       map[string]any           `json:"fields"`
	Score        int                      `json:"score"`
	Outcome      map[string]any           `json:"outcome,omitempty"`
	Rewards      map[string]*ResolvedRule `json:"rewards,omitempty"`
	StartedAtMs  int64                    `json:"started_at_ms,omitempty"`
	DeadlineMs   int64                    `json:"deadline_ms,omitempty"`
}

// BoardRow is a single read-only ranked/curated entry on a Board.
type BoardRow struct {
	Rank         int            `json:"rank"`
	EntryID      string         `json:"entry_id"`
	SubmissionID string         `json:"submission_id,omitempty"`
	SortValue    float64        `json:"sort_value"`
	Display      map[string]any `json:"display"`
	CreatedAtMs  int64          `json:"created_at_ms"`
}

// BoardView represents the current active state of one named Board.
type BoardView struct {
	BoardID     string     `json:"board_id"`
	Title       string     `json:"title"`
	Rows        []BoardRow `json:"rows"`
	UpdatedAtMs int64      `json:"updated_at_ms"`
}

// Snapshot is the authoritative state frame pushed to tablets over WS /v1/events and GET /v1/state.
type Snapshot struct {
	Type           string                 `json:"type"`
	BootID         string                 `json:"boot_id"`
	Seq            int64                  `json:"seq"`
	Phase          string                 `json:"phase"`
	SchemaHash     string                 `json:"schema_hash"`
	Versions       VersionsInfo           `json:"versions"`
	DisplayOnline  bool                   `json:"display_online"`
	ClientsOnline  int                    `json:"clients_online"`
	CloudOnline    bool                   `json:"cloud_online"`
	Wifi           *WifiInfo              `json:"wifi,omitempty"`
	ConfigRevision int                    `json:"config_revision"`
	Round          *ActiveRound           `json:"round,omitempty"`
	Boards         map[string]BoardView   `json:"boards,omitempty"`
	Catalogs       map[string]CatalogView `json:"catalogs,omitempty"`
	Rulesets       map[string]RulesetView `json:"rulesets,omitempty"`
	ServerMs       int64                  `json:"server_ms"`
}

// SubmissionRecord represents an immutable historical form entry.
type SubmissionRecord struct {
	ID             string         `json:"id"`
	RequestID      string         `json:"request_id"`
	FormID         string         `json:"form_id"`
	FormVersion    int            `json:"form_version"`
	Payload        map[string]any `json:"payload"`
	SourceDeviceID string         `json:"source_device_id"`
	SourceDevice   string         `json:"source_device_name,omitempty"`
	RoundID        string         `json:"round_id,omitempty"`
	Status         string         `json:"status"`
	Outcome        map[string]any `json:"outcome,omitempty"`
	CreatedAtMs    int64          `json:"created_at_ms"`
}

// BridgeCapabilities lets a connected game dynamically register extra runtime settings,
// game-targeted actions, or game-managed boards during its `ready` handshake.
type BridgeCapabilities struct {
	Settings []SettingSpec `json:"settings,omitempty"`
	Actions  []ActionSpec  `json:"actions,omitempty"`
	Boards   []BoardSpec   `json:"boards,omitempty"`
}

// BridgeMessage is the canonical envelope over loopback WS /v1/bridge (Game <-> ckserver).
type BridgeMessage struct {
	T               string              `json:"t"`
	GameName        string              `json:"game_name,omitempty"`
	GameVersion     string              `json:"game_version,omitempty"`
	EngineVersion   string              `json:"engine_version,omitempty"`
	BridgeProtocol  int                 `json:"bridge_protocol,omitempty"`
	Capabilities    *BridgeCapabilities `json:"capabilities,omitempty"`
	RoundID         string              `json:"round_id,omitempty"`
	Phase           string              `json:"phase,omitempty"`
	Score           int                 `json:"score"`
	Reason          string              `json:"reason,omitempty"`
	Revision        int                 `json:"revision,omitempty"`
	Values          map[string]any      `json:"values,omitempty"`
	RuntimeValues   map[string]any      `json:"runtime_values,omitempty"`
	Changed         []string            `json:"changed,omitempty"`
	CallID          string              `json:"call_id,omitempty"`
	ActionID        string              `json:"action_id,omitempty"`
	Params          map[string]any      `json:"params,omitempty"`
	OK              null.Bool           `json:"ok"`
	Message         string              `json:"message,omitempty"`
	Data            map[string]any      `json:"data,omitempty"`
	Board           *BoardSpec          `json:"board,omitempty"`
	BoardID         string              `json:"board_id,omitempty"`
	EntryID         string              `json:"entry_id,omitempty"`
	SubmissionID    string              `json:"submission_id,omitempty"`
	SortValue       float64             `json:"sort_value,omitempty"`
	Display         map[string]any      `json:"display,omitempty"`
	Rows            []BoardRow               `json:"rows,omitempty"`
	Catalogs        map[string]CatalogView   `json:"catalogs,omitempty"`
	Rulesets        map[string]RulesetView   `json:"rulesets,omitempty"`
	Catalog         *CatalogView             `json:"catalog,omitempty"`
	Ruleset         *RulesetView             `json:"ruleset,omitempty"`
	Outcome         map[string]any           `json:"outcome,omitempty"`
	Rewards         map[string]*ResolvedRule `json:"rewards,omitempty"`
	Round           *ActiveRound             `json:"round,omitempty"`
	Versions        *VersionsInfo            `json:"versions,omitempty"`
	Snapshot        *Snapshot                `json:"snapshot,omitempty"`
	Address         string                   `json:"address,omitempty"`
	PairingCode     string                   `json:"pairing_code,omitempty"`
	ClientsOnline   null.Int                 `json:"clients_online"`
}
