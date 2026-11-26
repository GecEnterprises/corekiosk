package ckframework

// PhaseSpec describes one state in a kiosk activation loop.
type PhaseSpec struct {
	Name            string `json:"name"`
	Label           string `json:"label"`
	GuardTimeoutSec int    `json:"guard_timeout_sec,omitempty"`
	FallbackPhase   string `json:"fallback_phase,omitempty"`
	AllowSubmit     bool   `json:"allow_submit"`
	AllowCancel     bool   `json:"allow_cancel"`
}

const (
	PhaseIdle         = "idle"
	PhaseAwaitingHand = "awaiting_hand"
	PhaseCountdown    = "countdown"
	PhasePlaying      = "playing"
	PhaseResults      = "results"
)

// StandardArcadeLoop returns the canonical 5-phase arcade activation state machine.
func StandardArcadeLoop() []PhaseSpec {
	return []PhaseSpec{
		{Name: PhaseIdle, Label: "Ready", AllowSubmit: true, AllowCancel: false},
		{Name: PhaseAwaitingHand, Label: "Waiting for player", GuardTimeoutSec: 65, FallbackPhase: PhaseIdle, AllowSubmit: false, AllowCancel: true},
		{Name: PhaseCountdown, Label: "Countdown", GuardTimeoutSec: 20, FallbackPhase: PhaseIdle, AllowSubmit: false, AllowCancel: true},
		{Name: PhasePlaying, Label: "Playing", GuardTimeoutSec: 300, FallbackPhase: PhaseIdle, AllowSubmit: false, AllowCancel: true},
		{Name: PhaseResults, Label: "Results", GuardTimeoutSec: 20, FallbackPhase: PhaseIdle, AllowSubmit: false, AllowCancel: false},
	}
}

// WallLoop returns a single-phase non-blocking submission loop (e.g. live wishing wall / photobooth queue).
func WallLoop() []PhaseSpec {
	return []PhaseSpec{
		{Name: PhaseIdle, Label: "Ready", AllowSubmit: true, AllowCancel: false},
	}
}
