package ckframework

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
)

// AppSpec is the declarative blueprint for a corekiosk activation.
type AppSpec struct {
	Name             string          `json:"name"`
	Version          string          `json:"version"`
	MinClientVersion string          `json:"min_client_version,omitempty"`
	Phases           []PhaseSpec     `json:"phases"`
	Forms            []FormSpec      `json:"forms"`
	Settings         []SettingSpec   `json:"settings"`
	Actions          []ActionSpec    `json:"actions"`
	Boards           []BoardSpec     `json:"boards"`
	Catalogs         []CatalogSpec   `json:"catalogs,omitempty"`
	Rulesets         []RulesetSpec   `json:"rulesets,omitempty"`
	seedAssets       []SeedAssetSpec
}

// NewApp creates an AppSpec with StandardArcadeLoop as the default phase list.
func NewApp(name, version string) *AppSpec {
	return &AppSpec{
		Name:    name,
		Version: version,
		Phases:  StandardArcadeLoop(),
	}
}

func (a *AppSpec) RequireMinClientVersion(v string) *AppSpec {
	a.MinClientVersion = v
	return a
}

func (a *AppSpec) WithPhases(phases []PhaseSpec) *AppSpec {
	a.Phases = phases
	return a
}

// WithForm registers the primary registration form (id="registration") with CKTelemetryTransmit=true by default.
func (a *AppSpec) WithForm(fields ...FormField) *AppSpec {
	return a.WithFormSpec(NewForm("registration", "Registration", fields...))
}

func (a *AppSpec) WithFormSpec(form FormSpec) *AppSpec {
	for i, existing := range a.Forms {
		if existing.ID == form.ID {
			a.Forms[i] = form
			return a
		}
	}
	a.Forms = append(a.Forms, form)
	return a
}

func (a *AppSpec) WithSettings(settings ...SettingSpec) *AppSpec {
	a.Settings = append(a.Settings, settings...)
	return a
}

func (a *AppSpec) WithActions(actions ...ActionSpec) *AppSpec {
	a.Actions = append(a.Actions, actions...)
	return a
}

func (a *AppSpec) WithBoards(boards ...BoardSpec) *AppSpec {
	for _, b := range boards {
		a.Boards = append(a.Boards, b)
		if b.AllowBoardClear {
			actionID := "board." + b.ID + ".clear"
			if _, ok := a.ActionByID(actionID); !ok {
				a.Actions = append(a.Actions,
					Action(actionID, "Boards", "Clear "+b.Title).
						WithHelp("Removes all active rows from "+b.Title+". Historical form submissions are preserved.").
						TargetServer().
						Danger().
						ConfirmDialog("Clear all active rows from "+b.Title+"? Historical form data will remain intact."),
				)
			}
		}
	}
	return a
}

func (a *AppSpec) WithSeedAssets(assets ...SeedAssetSpec) *AppSpec {
	a.seedAssets = append(a.seedAssets, assets...)
	return a
}

func (a *AppSpec) SeedAssets() []SeedAssetSpec {
	return a.seedAssets
}

func (a *AppSpec) WithCatalogs(catalogs ...CatalogSpec) *AppSpec {
	for _, c := range catalogs {
		c = c.inferDisplayFields()
		a.Catalogs = append(a.Catalogs, c)
		if len(c.Defaults) > 0 {
			actionID := "catalog." + c.ID + ".reset_defaults"
			if _, ok := a.ActionByID(actionID); !ok {
				a.Actions = append(a.Actions,
					Action(actionID, "Catalogs", "Reset "+c.Title).
						WithHelp("Restores "+c.Title+" back to its built-in default entries.").
						TargetServer().
						Danger().
						ConfirmDialog("Reset "+c.Title+" to default entries? Custom edits will be replaced."),
				)
			}
		}
	}
	return a
}

func (a *AppSpec) WithRulesets(rulesets ...RulesetSpec) *AppSpec {
	for _, r := range rulesets {
		a.Rulesets = append(a.Rulesets, r)
		if len(r.Defaults) > 0 {
			actionID := "ruleset." + r.ID + ".reset_defaults"
			if _, ok := a.ActionByID(actionID); !ok {
				a.Actions = append(a.Actions,
					Action(actionID, "Rulesets", "Reset "+r.Title).
						WithHelp("Restores "+r.Title+" back to its built-in default thresholds and mappings.").
						TargetServer().
						Danger().
						ConfirmDialog("Reset "+r.Title+" to default rules?"),
				)
			}
		}
	}
	return a
}

func (a *AppSpec) DefaultForm() (FormSpec, bool) {
	if len(a.Forms) == 0 {
		return FormSpec{}, false
	}
	return a.Forms[0], true
}

func (a *AppSpec) FormByID(id string) (FormSpec, bool) {
	if id == "" {
		return a.DefaultForm()
	}
	for _, f := range a.Forms {
		if f.ID == id {
			return f, true
		}
	}
	return FormSpec{}, false
}

func (a *AppSpec) SettingByKey(key string) (SettingSpec, bool) {
	for _, s := range a.Settings {
		if s.Key == key {
			return s, true
		}
	}
	return SettingSpec{}, false
}

func (a *AppSpec) ActionByID(id string) (ActionSpec, bool) {
	for _, act := range a.Actions {
		if act.ID == id {
			return act, true
		}
	}
	return ActionSpec{}, false
}

func (a *AppSpec) BoardByID(id string) (BoardSpec, bool) {
	for _, b := range a.Boards {
		if b.ID == id {
			return b, true
		}
	}
	return BoardSpec{}, false
}

func (a *AppSpec) CatalogByID(id string) (CatalogSpec, bool) {
	for _, c := range a.Catalogs {
		if c.ID == id {
			return c, true
		}
	}
	return CatalogSpec{}, false
}

func (a *AppSpec) RulesetByID(id string) (RulesetSpec, bool) {
	for _, r := range a.Rulesets {
		if r.ID == id {
			return r, true
		}
	}
	return RulesetSpec{}, false
}

func (a *AppSpec) PhaseByName(name string) (PhaseSpec, bool) {
	for _, p := range a.Phases {
		if p.Name == name {
			return p, true
		}
	}
	return PhaseSpec{}, false
}

// ArmPhase returns the first active phase entered when a round is armed (first phase with !AllowSubmit,
// falling back to PhaseAwaitingHand if none is declared).
func (a *AppSpec) ArmPhase() string {
	for _, p := range a.Phases {
		if !p.AllowSubmit {
			return p.Name
		}
	}
	return PhaseAwaitingHand
}

// IsLivePhase reports whether entering phase name marks an armed round as actively live.
func (a *AppSpec) IsLivePhase(name string) bool {
	if name == PhasePlaying {
		return true
	}
	if len(a.Phases) > 2 && name != PhaseIdle && name != PhaseResults && name != a.ArmPhase() {
		return true
	}
	return false
}

// DefaultSettings returns default values split into persisted and runtime maps.
func (a *AppSpec) DefaultSettings() (persisted map[string]any, runtime map[string]any) {
	persisted = make(map[string]any)
	runtime = make(map[string]any)
	for _, s := range a.Settings {
		if s.Scope == ScopeRuntime {
			runtime[s.Key] = s.Default
		} else {
			persisted[s.Key] = s.Default
		}
	}
	return persisted, runtime
}

// ValidateConfigPatch validates a partial or full settings update against known SettingSpecs
// (including optional dynamic game-declared settings).
func ValidateConfigPatch(specs []SettingSpec, currentPersisted, currentRuntime, patch map[string]any) (
	nextPersisted map[string]any,
	nextRuntime map[string]any,
	changed []string,
	persistedChanged bool,
	err error,
) {
	index := make(map[string]SettingSpec, len(specs))
	for _, s := range specs {
		index[s.Key] = s
	}
	nextPersisted = maps.Clone(currentPersisted)
	if nextPersisted == nil {
		nextPersisted = map[string]any{}
	}
	nextRuntime = maps.Clone(currentRuntime)
	if nextRuntime == nil {
		nextRuntime = map[string]any{}
	}
	for k, raw := range patch {
		spec, ok := index[k]
		if !ok {
			return nil, nil, nil, false, ErrValidation.New("unknown setting %q", k).WithProperty(PropCode, "unknown_setting")
		}
		norm, verr := spec.NormalizeValue(raw)
		if verr != nil {
			return nil, nil, nil, false, verr
		}
		changed = append(changed, k)
		if spec.Scope == ScopeRuntime {
			nextRuntime[k] = norm
		} else {
			nextPersisted[k] = norm
			persistedChanged = true
		}
	}
	return nextPersisted, nextRuntime, changed, persistedChanged, nil
}

// SchemaSnapshot is the payload returned by GET /v1/schema (including dynamic game capabilities).
type SchemaSnapshot struct {
	AppName          string        `json:"app_name"`
	AppVersion       string        `json:"app_version"`
	FrameworkVersion string        `json:"framework_version"`
	ProtocolVersion  int           `json:"protocol_version"`
	MinClientVersion string        `json:"min_client_version,omitempty"`
	SchemaHash       string        `json:"schema_hash"`
	Phases           []PhaseSpec   `json:"phases"`
	Forms            []FormSpec    `json:"forms"`
	Settings         []SettingSpec `json:"settings"`
	Actions          []ActionSpec  `json:"actions"`
	Boards           []BoardSpec   `json:"boards"`
	Catalogs         []CatalogSpec `json:"catalogs,omitempty"`
	Rulesets         []RulesetSpec `json:"rulesets,omitempty"`
}

// ComputeSchemaHash produces a deterministic short SHA-256 fingerprint of the active schema.
func ComputeSchemaHash(snap SchemaSnapshot) string {
	snap.SchemaHash = ""
	raw, _ := json.Marshal(snap)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}
