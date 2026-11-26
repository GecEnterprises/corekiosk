package ckframework

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// FieldType defines supported form & action-parameter field types.
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldTextArea FieldType = "textarea"
	FieldEmail    FieldType = "email"
	FieldHandle   FieldType = "handle"
	FieldPhone    FieldType = "phone"
	FieldNumber   FieldType = "number"
	FieldEnum     FieldType = "enum"
	FieldBool     FieldType = "bool"
)

// FormField describes one input field in a registration form or action parameter sheet.
type FormField struct {
	Key         string       `json:"key"`
	Label       string       `json:"label"`
	Type        FieldType    `json:"type"`
	Required    bool         `json:"required,omitempty"`
	MinLen      int          `json:"min_len,omitempty"`
	MaxLen      int          `json:"max_len,omitempty"`
	Min         *float64     `json:"min,omitempty"`
	Max         *float64     `json:"max,omitempty"`
	Prefix      string       `json:"prefix,omitempty"`
	Placeholder string       `json:"placeholder,omitempty"`
	Help        string       `json:"help,omitempty"`
	Pattern     string       `json:"pattern,omitempty"`
	Options     []EnumOption `json:"options,omitempty"`
	Default     any          `json:"default,omitempty"`
}

// FormSpec declares a kiosk form. Every form defaults to TelemetryTransmit=true
// (CKTelemetryTransmit), saving each submission atomically into the telemetry outbox
// for table-shaped Firestore sync.
type FormSpec struct {
	ID                string      `json:"id"`
	Title             string      `json:"title"`
	Version           int         `json:"version"`
	TelemetryTransmit bool        `json:"telemetry_transmit"`
	Fields            []FormField `json:"fields"`
}

// NewForm creates a FormSpec with CKTelemetryTransmit enabled by default.
func NewForm(id, title string, fields ...FormField) FormSpec {
	return FormSpec{
		ID:                id,
		Title:             title,
		Version:           1,
		TelemetryTransmit: true,
		Fields:            fields,
	}
}

// WithVersion sets the schema version of the form.
func (f FormSpec) WithVersion(v int) FormSpec {
	f.Version = v
	return f
}

// WithoutTelemetryTransmit opts this form out of automatic CKTelemetryTransmit cloud sync.
func (f FormSpec) WithoutTelemetryTransmit() FormSpec {
	f.TelemetryTransmit = false
	return f
}

func TextField(key, label string) FormField {
	return FormField{Key: key, Label: label, Type: FieldText}
}

func TextAreaField(key, label string) FormField {
	return FormField{Key: key, Label: label, Type: FieldTextArea}
}

func EmailField(key, label string) FormField {
	return FormField{Key: key, Label: label, Type: FieldEmail}
}

func HandleField(key, label string) FormField {
	return FormField{Key: key, Label: label, Type: FieldHandle}
}

func PhoneField(key, label string) FormField {
	return FormField{Key: key, Label: label, Type: FieldPhone}
}

func NumberFormField(key, label string, min, max float64) FormField {
	return FormField{Key: key, Label: label, Type: FieldNumber, Min: &min, Max: &max}
}

func EnumFormField(key, label string, options []EnumOption) FormField {
	return FormField{Key: key, Label: label, Type: FieldEnum, Options: options}
}

func BoolFormField(key, label string, def bool) FormField {
	return FormField{Key: key, Label: label, Type: FieldBool, Default: def}
}

func (f FormField) RequiredField() FormField        { f.Required = true; return f }
func (f FormField) MaxLength(n int) FormField       { f.MaxLen = n; return f }
func (f FormField) MinLength(n int) FormField       { f.MinLen = n; return f }
func (f FormField) WithPrefix(p string) FormField   { f.Prefix = p; return f }
func (f FormField) WithPlaceholder(p string) FormField { f.Placeholder = p; return f }
func (f FormField) WithHelp(h string) FormField     { f.Help = h; return f }
func (f FormField) WithPattern(p string) FormField  { f.Pattern = p; return f }
func (f FormField) WithDefault(v any) FormField     { f.Default = v; return f }

// ValidateAndClean validates and normalizes a raw field map against a slice of FormFields.
func ValidateFields(fields []FormField, raw map[string]any) (map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	clean := make(map[string]any, len(fields))
	fieldErrors := map[string]string{}

	for _, f := range fields {
		v, exists := raw[f.Key]
		if !exists || v == nil {
			if f.Default != nil {
				v = f.Default
				exists = true
			}
		}

		switch f.Type {
		case FieldBool:
			if !exists || v == nil {
				if f.Required {
					fieldErrors[f.Key] = fmt.Sprintf("%s is required", f.Label)
					continue
				}
				clean[f.Key] = false
				continue
			}
			b, ok := v.(bool)
			if !ok {
				fieldErrors[f.Key] = fmt.Sprintf("%s must be true or false", f.Label)
				continue
			}
			clean[f.Key] = b

		case FieldNumber:
			if !exists || v == nil {
				if f.Required {
					fieldErrors[f.Key] = fmt.Sprintf("%s is required", f.Label)
				}
				continue
			}
			num, ok := toFloat(v)
			if !ok || math.IsNaN(num) || math.IsInf(num, 0) {
				fieldErrors[f.Key] = fmt.Sprintf("%s must be a number", f.Label)
				continue
			}
			if f.Min != nil && num < *f.Min {
				fieldErrors[f.Key] = fmt.Sprintf("%s must be at least %g", f.Label, *f.Min)
				continue
			}
			if f.Max != nil && num > *f.Max {
				fieldErrors[f.Key] = fmt.Sprintf("%s must be at most %g", f.Label, *f.Max)
				continue
			}
			clean[f.Key] = num

		case FieldEnum:
			s, _ := v.(string)
			s = strings.TrimSpace(s)
			if s == "" {
				if f.Required {
					fieldErrors[f.Key] = fmt.Sprintf("%s is required", f.Label)
				} else {
					clean[f.Key] = ""
				}
				continue
			}
			allowed := make([]string, 0, len(f.Options))
			for _, opt := range f.Options {
				allowed = append(allowed, opt.Value)
			}
			if !slices.Contains(allowed, s) {
				fieldErrors[f.Key] = fmt.Sprintf("%s has an invalid selection", f.Label)
				continue
			}
			clean[f.Key] = s

		default: // text, textarea, email, handle, phone
			var s string
			if exists && v != nil {
				str, ok := v.(string)
				if !ok {
					fieldErrors[f.Key] = fmt.Sprintf("%s must be text", f.Label)
					continue
				}
				s = strings.TrimSpace(str)
				if f.Type == FieldHandle && f.Prefix != "" {
					s = strings.TrimPrefix(s, f.Prefix)
				}
			}
			length := utf8.RuneCountInString(s)
			if length == 0 {
				if f.Required {
					fieldErrors[f.Key] = fmt.Sprintf("%s is required", f.Label)
					continue
				}
				clean[f.Key] = ""
				continue
			}
			if f.MinLen > 0 && length < f.MinLen {
				fieldErrors[f.Key] = fmt.Sprintf("%s must be at least %d characters", f.Label, f.MinLen)
				continue
			}
			if f.MaxLen > 0 && length > f.MaxLen {
				fieldErrors[f.Key] = fmt.Sprintf("%s must be at most %d characters", f.Label, f.MaxLen)
				continue
			}
			if f.Type == FieldEmail {
				at := strings.Index(s, "@")
				if at <= 0 || at == len(s)-1 || !strings.Contains(s[at+1:], ".") || strings.ContainsAny(s, " \t\r\n") {
					fieldErrors[f.Key] = "Enter a valid email address"
					continue
				}
			}
			if f.Pattern != "" {
				re, err := regexp.Compile(f.Pattern)
				if err == nil && !re.MatchString(s) {
					fieldErrors[f.Key] = fmt.Sprintf("%s format is invalid", f.Label)
					continue
				}
			}
			clean[f.Key] = s
		}
	}

	if len(fieldErrors) > 0 {
		return nil, ErrValidation.New("invalid form submission").
			WithProperty(PropCode, "invalid_form").
			WithProperty(PropDetails, fieldErrors)
	}
	return clean, nil
}
