package ckframework

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
)

// SettingType defines the data type and UI control rendered for an operator setting.
type SettingType string

const (
	SettingBool      SettingType = "bool"
	SettingInt       SettingType = "int"
	SettingNumber    SettingType = "number"
	SettingEnum      SettingType = "enum"
	SettingMultiEnum SettingType = "multi_enum"
	SettingText      SettingType = "text"
	SettingColor     SettingType = "color"
	SettingImage     SettingType = "image"
)

// SettingScope distinguishes persisted settings (SQLite) from ephemeral runtime toggles.
type SettingScope string

const (
	ScopePersisted SettingScope = "persisted"
	ScopeRuntime   SettingScope = "runtime"
)

// ApplyPolicy declares when a changed setting takes effect in the game/server.
type ApplyPolicy string

const (
	ApplyImmediate ApplyPolicy = "immediate"
	ApplyNextRound ApplyPolicy = "next_round"
	ApplyRestart   ApplyPolicy = "restart"
)

// EnumOption provides a wire value plus human-friendly label & optional description.
type EnumOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// VisibilityRule conditionally shows a setting in the Operator UI when Key == Equals.
type VisibilityRule struct {
	Key    string `json:"key"`
	Equals any    `json:"equals"`
}

// SettingSpec declares one operator-tunable setting.
type SettingSpec struct {
	Key         string          `json:"key"`
	Group       string          `json:"group"`
	Label       string          `json:"label"`
	Help        string          `json:"help,omitempty"`
	Type        SettingType     `json:"type"`
	Scope       SettingScope    `json:"scope"`
	Apply       ApplyPolicy     `json:"apply"`
	Min         *float64        `json:"min,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	Step        *float64        `json:"step,omitempty"`
	Precision   int             `json:"precision,omitempty"`
	Unit        string          `json:"unit,omitempty"`
	MaxLen      int             `json:"max_len,omitempty"`
	Multiline   bool            `json:"multiline,omitempty"`
	Placeholder string          `json:"placeholder,omitempty"`
	Options     []EnumOption    `json:"options,omitempty"`
	Presets     []string        `json:"presets,omitempty"`
	VisibleIf   *VisibilityRule `json:"visible_if,omitempty"`
	Default     any             `json:"default"`
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)

func BoolSetting(key, group, label string, def bool) SettingSpec {
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingBool, Scope: ScopePersisted, Apply: ApplyImmediate, Default: def}
}

func IntSetting(key, group, label string, min, max, step int, unit string, def int) SettingSpec {
	fMin, fMax, fStep := float64(min), float64(max), float64(step)
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingInt, Scope: ScopePersisted, Apply: ApplyImmediate, Min: &fMin, Max: &fMax, Step: &fStep, Unit: unit, Default: def}
}

func NumberSetting(key, group, label string, min, max, step float64, unit string, def float64) SettingSpec {
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingNumber, Scope: ScopePersisted, Apply: ApplyImmediate, Min: &min, Max: &max, Step: &step, Unit: unit, Default: def}
}

func EnumSetting(key, group, label string, options []EnumOption, def string) SettingSpec {
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingEnum, Scope: ScopePersisted, Apply: ApplyImmediate, Options: options, Default: def}
}

func MultiEnumSetting(key, group, label string, options []EnumOption, def []string) SettingSpec {
	if def == nil {
		def = []string{}
	}
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingMultiEnum, Scope: ScopePersisted, Apply: ApplyImmediate, Options: options, Default: def}
}

func TextSetting(key, group, label string, def string) SettingSpec {
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingText, Scope: ScopePersisted, Apply: ApplyImmediate, Default: def}
}

func ColorSetting(key, group, label string, def string, presets ...string) SettingSpec {
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingColor, Scope: ScopePersisted, Apply: ApplyImmediate, Default: def, Presets: presets}
}

func ImageSetting(key, group, label string, defAssetID string) SettingSpec {
	return SettingSpec{Key: key, Group: group, Label: label, Type: SettingImage, Scope: ScopePersisted, Apply: ApplyImmediate, Default: defAssetID}
}

func (s SettingSpec) WithHelp(h string) SettingSpec                 { s.Help = h; return s }
func (s SettingSpec) RuntimeOnly() SettingSpec                      { s.Scope = ScopeRuntime; return s }
func (s SettingSpec) ApplyImmediate() SettingSpec                   { s.Apply = ApplyImmediate; return s }
func (s SettingSpec) ApplyNextRound() SettingSpec                   { s.Apply = ApplyNextRound; return s }
func (s SettingSpec) ApplyRestart() SettingSpec                     { s.Apply = ApplyRestart; return s }
func (s SettingSpec) WithMaxLen(n int) SettingSpec                  { s.MaxLen = n; return s }
func (s SettingSpec) WithPlaceholder(p string) SettingSpec          { s.Placeholder = p; return s }
func (s SettingSpec) VisibleIfRule(key string, eq any) SettingSpec  { s.VisibleIf = &VisibilityRule{Key: key, Equals: eq}; return s }

// NormalizeValue validates and canonicalizes a single setting value.
func (s SettingSpec) NormalizeValue(raw any) (any, error) {
	switch s.Type {
	case SettingBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, ErrValidation.New("%s must be true or false", s.Label)
		}
		return b, nil

	case SettingEnum:
		str, ok := raw.(string)
		if !ok {
			return nil, ErrValidation.New("%s must be a string", s.Label)
		}
		for _, opt := range s.Options {
			if opt.Value == str {
				return str, nil
			}
		}
		return nil, ErrValidation.New("%s has unsupported option %q", s.Label, str)

	case SettingMultiEnum:
		var list []string
		switch typed := raw.(type) {
		case []string:
			list = typed
		case []any:
			list = make([]string, 0, len(typed))
			for _, item := range typed {
				str, ok := item.(string)
				if !ok {
					return nil, ErrValidation.New("%s entries must be strings", s.Label)
				}
				list = append(list, str)
			}
		default:
			return nil, ErrValidation.New("%s must be a list of options", s.Label)
		}
		allowed := make([]string, 0, len(s.Options))
		for _, opt := range s.Options {
			allowed = append(allowed, opt.Value)
		}
		out := make([]string, 0, len(list))
		for _, item := range list {
			if !slices.Contains(allowed, item) {
				return nil, ErrValidation.New("%s contains unsupported option %q", s.Label, item)
			}
			if !slices.Contains(out, item) {
				out = append(out, item)
			}
		}
		return out, nil

	case SettingText:
		str, ok := raw.(string)
		if !ok {
			return nil, ErrValidation.New("%s must be text", s.Label)
		}
		if s.MaxLen > 0 && len([]rune(str)) > s.MaxLen {
			return nil, ErrValidation.New("%s exceeds %d characters", s.Label, s.MaxLen)
		}
		return str, nil

	case SettingColor:
		str, ok := raw.(string)
		if !ok || !hexColorRe.MatchString(str) {
			return nil, ErrValidation.New("%s must be a hex color (#RRGGBB)", s.Label)
		}
		return str, nil

	case SettingImage:
		str, ok := raw.(string)
		if !ok || len(str) > 128 {
			return nil, ErrValidation.New("%s must be an asset id string", s.Label)
		}
		return str, nil

	case SettingInt, SettingNumber:
		num, ok := toFloat(raw)
		if !ok || math.IsNaN(num) || math.IsInf(num, 0) {
			return nil, ErrValidation.New("%s must be a finite number", s.Label)
		}
		if s.Type == SettingInt && num != math.Trunc(num) {
			return nil, ErrValidation.New("%s must be a whole number", s.Label)
		}
		if s.Min != nil && num < *s.Min {
			return nil, ErrValidation.New("%s must be >= %g", s.Label, *s.Min)
		}
		if s.Max != nil && num > *s.Max {
			return nil, ErrValidation.New("%s must be <= %g", s.Label, *s.Max)
		}
		if s.Type == SettingInt {
			return int(math.Round(num)), nil
		}
		return num, nil

	default:
		return nil, ErrValidation.New("unknown setting type %q for %s", s.Type, s.Key)
	}
}

func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// FormatSettingSummary returns a human-readable string for logs/audit trails.
func FormatSettingSummary(key string, val any) string {
	return fmt.Sprintf("%s=%v", key, val)
}
