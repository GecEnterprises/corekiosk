package ckframework

// ActionTarget identifies which runtime executes an ActionSpec trigger.
type ActionTarget string

const (
	TargetServer   ActionTarget = "server"
	TargetGame     ActionTarget = "game"
	TargetLauncher ActionTarget = "launcher"
)

// ActionStyle hints how the tablet/launcher UI styles the action button.
type ActionStyle string

const (
	ActionStyleDefault ActionStyle = "default"
	ActionStylePrimary ActionStyle = "primary"
	ActionStyleDanger  ActionStyle = "danger"
)

// ConfirmPolicy controls confirmation UX before firing an action.
type ConfirmPolicy string

const (
	ConfirmNone        ConfirmPolicy = "none"
	ConfirmDialog      ConfirmPolicy = "dialog"
	ConfirmTypeConfirm ConfirmPolicy = "type_confirm"
)

// ActionSpec declares a stateless admin trigger / command (not a persisted config value).
type ActionSpec struct {
	ID            string        `json:"id"`
	Group         string        `json:"group"`
	Label         string        `json:"label"`
	Help          string        `json:"help,omitempty"`
	Target        ActionTarget  `json:"target"`
	Style         ActionStyle   `json:"style"`
	Confirm       ConfirmPolicy `json:"confirm"`
	ConfirmText   string        `json:"confirm_text,omitempty"`
	Params        []FormField   `json:"params,omitempty"`
	EnabledPhases []string      `json:"enabled_phases,omitempty"`
	CooldownMs    int           `json:"cooldown_ms,omitempty"`
}

// Action creates a new stateless admin trigger defaulting to server target & no confirmation.
func Action(id, group, label string) ActionSpec {
	return ActionSpec{
		ID:      id,
		Group:   group,
		Label:   label,
		Target:  TargetServer,
		Style:   ActionStyleDefault,
		Confirm: ConfirmNone,
	}
}

func (a ActionSpec) WithHelp(h string) ActionSpec { a.Help = h; return a }
func (a ActionSpec) TargetServer() ActionSpec     { a.Target = TargetServer; return a }
func (a ActionSpec) TargetGame() ActionSpec       { a.Target = TargetGame; return a }
func (a ActionSpec) TargetLauncher() ActionSpec   { a.Target = TargetLauncher; return a }
func (a ActionSpec) Primary() ActionSpec          { a.Style = ActionStylePrimary; return a }
func (a ActionSpec) Danger() ActionSpec           { a.Style = ActionStyleDanger; return a }
func (a ActionSpec) ConfirmDialog(msg string) ActionSpec {
	a.Confirm = ConfirmDialog
	a.ConfirmText = msg
	return a
}
func (a ActionSpec) ConfirmTypeWord(msg string) ActionSpec {
	a.Confirm = ConfirmTypeConfirm
	a.ConfirmText = msg
	return a
}
func (a ActionSpec) WithParams(fields ...FormField) ActionSpec   { a.Params = fields; return a }
func (a ActionSpec) EnabledInPhases(phases ...string) ActionSpec { a.EnabledPhases = phases; return a }
func (a ActionSpec) Cooldown(ms int) ActionSpec                  { a.CooldownMs = ms; return a }

// ActionResult is returned to the tablet after executing an ActionSpec.
type ActionResult struct {
	OK      bool           `json:"ok"`
	Action  string         `json:"action"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}
