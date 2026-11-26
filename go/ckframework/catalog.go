package ckframework

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var entryIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// CatalogEntry is one mutable operator-managed record in a CatalogSpec (e.g. a prize item).
type CatalogEntry struct {
	ID          string         `json:"id"`
	Order       int            `json:"order"`
	Enabled     bool           `json:"enabled"`
	Fields      map[string]any `json:"fields"`
	UpdatedAtMs int64          `json:"updated_at_ms,omitempty"`
}

// CatalogSpec declares an operator-editable lookup table ("constant entries") such as prizes,
// sponsor items, or trivia cards. Row payloads are validated using FormField specs.
type CatalogSpec struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Help           string         `json:"help,omitempty"`
	Fields         []FormField    `json:"fields"`
	TitleField     string         `json:"title_field,omitempty"`
	ImageField     string         `json:"image_field,omitempty"`
	AllowAddDelete bool           `json:"allow_add_delete"`
	Defaults       []CatalogEntry `json:"defaults,omitempty"`
}

// CatalogView represents the live persisted state of one CatalogSpec in Snapshot and API responses.
type CatalogView struct {
	CatalogID   string         `json:"catalog_id"`
	Title       string         `json:"title"`
	Revision    int            `json:"revision"`
	Entries     []CatalogEntry `json:"entries"`
	UpdatedAtMs int64          `json:"updated_at_ms"`
}

// NewCatalog creates an operator-editable catalog allowing item add/delete by default.
func NewCatalog(id, title string, fields ...FormField) CatalogSpec {
	c := CatalogSpec{
		ID:             id,
		Title:          title,
		Fields:         fields,
		AllowAddDelete: true,
	}
	return c.inferDisplayFields()
}

// NewCatalogEntry constructs a default/seed CatalogEntry (enabled by default).
func NewCatalogEntry(id string, fields map[string]any) CatalogEntry {
	cp := make(map[string]any, len(fields))
	maps.Copy(cp, fields)
	return CatalogEntry{
		ID:      id,
		Enabled: true,
		Fields:  cp,
	}
}

func (e CatalogEntry) WithEnabled(enabled bool) CatalogEntry {
	e.Enabled = enabled
	return e
}

func (c CatalogSpec) inferDisplayFields() CatalogSpec {
	if c.TitleField == "" {
		for _, f := range c.Fields {
			if f.Type == FieldText {
				c.TitleField = f.Key
				break
			}
		}
	}
	if c.ImageField == "" {
		for _, f := range c.Fields {
			if f.Type == FieldImage {
				c.ImageField = f.Key
				break
			}
		}
	}
	return c
}

func (c CatalogSpec) WithHelp(h string) CatalogSpec       { c.Help = h; return c }
func (c CatalogSpec) WithTitleField(k string) CatalogSpec { c.TitleField = k; return c }
func (c CatalogSpec) WithImageField(k string) CatalogSpec { c.ImageField = k; return c }
func (c CatalogSpec) WithAddDelete(allow bool) CatalogSpec {
	c.AllowAddDelete = allow
	return c
}

func (c CatalogSpec) WithDefaults(entries ...CatalogEntry) CatalogSpec {
	out := make([]CatalogEntry, 0, len(entries))
	for i, e := range entries {
		e.Order = i
		if e.Fields == nil {
			e.Fields = map[string]any{}
		}
		out = append(out, e)
	}
	c.Defaults = out
	return c
}

// EntryTitle resolves the human-readable display label of a CatalogEntry.
func (c CatalogSpec) EntryTitle(e CatalogEntry) string {
	key := c.TitleField
	if key == "" {
		for _, f := range c.Fields {
			if f.Type == FieldText {
				key = f.Key
				break
			}
		}
	}
	if key != "" && e.Fields != nil {
		if s := strings.TrimSpace(fmt.Sprint(e.Fields[key])); s != "" && s != "<nil>" {
			return s
		}
	}
	return e.ID
}

// EntryImageID resolves the optional image asset_id of a CatalogEntry.
func (c CatalogSpec) EntryImageID(e CatalogEntry) string {
	key := c.ImageField
	if key == "" {
		for _, f := range c.Fields {
			if f.Type == FieldImage {
				key = f.Key
				break
			}
		}
	}
	if key != "" && e.Fields != nil {
		if s, ok := e.Fields[key].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// ValidateEntry validates and cleans a single CatalogEntry against the CatalogSpec.
func (c CatalogSpec) ValidateEntry(e CatalogEntry, order int, nowMs int64) (CatalogEntry, error) {
	id := strings.TrimSpace(e.ID)
	if !entryIDRe.MatchString(id) {
		return CatalogEntry{}, ErrValidation.New("invalid entry id %q (use letters, numbers, '-' or '_')", id).WithProperty(PropCode, "invalid_entry_id")
	}
	cleanFields, err := ValidateFields(c.Fields, e.Fields)
	if err != nil {
		return CatalogEntry{}, err
	}
	ts := e.UpdatedAtMs
	if nowMs > 0 {
		ts = nowMs
	}
	return CatalogEntry{
		ID:          id,
		Order:       order,
		Enabled:     e.Enabled,
		Fields:      cleanFields,
		UpdatedAtMs: ts,
	}, nil
}

// NormalizeEntries validates a full slice of entries, ensuring unique IDs and sequential Order.
func (c CatalogSpec) NormalizeEntries(entries []CatalogEntry, current []CatalogEntry, nowMs int64) ([]CatalogEntry, error) {
	seen := make(map[string]struct{}, len(entries))
	out := make([]CatalogEntry, 0, len(entries))
	for i, raw := range entries {
		clean, err := c.ValidateEntry(raw, i, nowMs)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[clean.ID]; dup {
			return nil, ErrValidation.New("duplicate catalog entry id %q", clean.ID).WithProperty(PropCode, "duplicate_entry_id")
		}
		seen[clean.ID] = struct{}{}
		out = append(out, clean)
	}

	if !c.AllowAddDelete && len(current) > 0 {
		curIDs := make([]string, 0, len(current))
		for _, ce := range current {
			curIDs = append(curIDs, ce.ID)
		}
		if len(out) != len(curIDs) {
			return nil, ErrForbidden.New("adding or deleting entries is disabled for %s", c.Title)
		}
		for _, item := range out {
			if !slices.Contains(curIDs, item.ID) {
				return nil, ErrForbidden.New("adding new entries is disabled for %s", c.Title)
			}
		}
	}
	return out, nil
}

// CloneCatalogEntries deep-copies a slice of CatalogEntry values.
func CloneCatalogEntries(in []CatalogEntry) []CatalogEntry {
	out := make([]CatalogEntry, len(in))
	for i, e := range in {
		cp := e
		cp.Fields = maps.Clone(e.Fields)
		if cp.Fields == nil {
			cp.Fields = map[string]any{}
		}
		out[i] = cp
	}
	return out
}
