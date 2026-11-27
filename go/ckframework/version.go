package ckframework

// Version is the lockstep SemVer version of the corekiosk framework & SDK.
const Version = "0.2.13"

// ProtocolVersion is the integer wire-contract version enforced on /v1/health,
// /v1/events, and /v1/bridge handshakes.
const ProtocolVersion = 1

// VersionsInfo reports the 4-track version metadata for an installation or device.
type VersionsInfo struct {
	App       string `json:"app,omitempty"`
	Framework string `json:"framework"`
	Launcher  string `json:"launcher,omitempty"`
	Game      string `json:"game,omitempty"`
	Protocol  int    `json:"protocol"`
}
