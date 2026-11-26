package ckframework

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// RulesetKind selects how rules in a RulesetSpec are structured and evaluated.
type RulesetKind string

const (
	// RulesetScoreTiers evaluates contiguous MinScore thresholds sorted high-to-low (gap-free tiers).
	RulesetScoreTiers RulesetKind = "score_tiers"
	// RulesetConditions evaluates ordered general field comparisons (gte, lte, eq, between).
	RulesetConditions RulesetKind = "conditions"
)

// RuleOp defines supported comparison operators for RulesetConditions.
type RuleOp string

const (
	OpGte     RuleOp = "gte"
	OpLte     RuleOp = "lte"
	OpEq      RuleOp = "eq"
	OpBetween RuleOp = "between"
)

// RuleEntry is one tier or condition rule inside a RulesetSpec.
type RuleEntry struct {
	ID        string         `json:"id"`
	Label     string         `json:"label,omitempty"`
	Enabled   bool           `json:"enabled"`
	MinScore  float64        `json:"min_score"`
	Field     string         `json:"field,omitempty"`
	Op        RuleOp         `json:"op,omitempty"`
	Value     any            `json:"value,omitempty"`
	MaxValue  *float64       `json:"max_value,omitempty"`
	CatalogID string         `json:"catalog_id,omitempty"`
	EntryID   string         `json:"entry_id,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
}

// RulesetSpec declares an operator-adjustable ruleset (e.g. score tiers -> prize catalog item)
// evaluated automatically when a gameplay round finishes.
type RulesetSpec struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Help           string      `json:"help,omitempty"`
	Kind           RulesetKind `json:"kind"`
	CatalogID      string      `json:"catalog_id,omitempty"`
	OutputFields   []FormField `json:"output_fields,omitempty"`
	AllowAddDelete bool        `json:"allow_add_delete"`
	SkipDisabled   bool        `json:"skip_disabled"`
	Defaults       []RuleEntry `json:"defaults,omitempty"`
}

// RulesetView represents the live persisted state of one RulesetSpec in Snapshot and API responses.
type RulesetView struct {
	RulesetID   string      `json:"ruleset_id"`
	Title       string      `json:"title"`
	Revision    int         `json:"revision"`
	Rules       []RuleEntry `json:"rules"`
	UpdatedAtMs int64       `json:"updated_at_ms"`
}

// ResolvedRule is the authoritative evaluated output attached to ActiveRound.Rewards & submission Outcome.
type ResolvedRule struct {
	RulesetID  string         `json:"ruleset_id"`
	RuleID     string         `json:"rule_id"`
	RuleLabel  string         `json:"rule_label,omitempty"`
	TierIndex  int            `json:"tier_index"`
	TotalTiers int            `json:"total_tiers"`
	MinScore   float64        `json:"min_score"`
	MaxScore   *float64       `json:"max_score,omitempty"`
	CatalogID  string         `json:"catalog_id,omitempty"`
	EntryID    string         `json:"entry_id,omitempty"`
	Title      string         `json:"title,omitempty"`
	ImageID    string         `json:"image_id,omitempty"`
	Entry      map[string]any `json:"entry,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
}

// NewScoreTierRuleset creates a gap-free threshold tier ruleset linked to a CatalogSpec (e.g. "prizes").
// SkipDisabled defaults to true so disabling an out-of-stock prize automatically cascades down.
func NewScoreTierRuleset(id, title, catalogID string) RulesetSpec {
	return RulesetSpec{
		ID:             id,
		Title:          title,
		Kind:           RulesetScoreTiers,
		CatalogID:      catalogID,
		AllowAddDelete: true,
		SkipDisabled:   true,
	}
}

// NewConditionsRuleset creates a general ordered condition ruleset.
func NewConditionsRuleset(id, title string) RulesetSpec {
	return RulesetSpec{
		ID:             id,
		Title:          title,
		Kind:           RulesetConditions,
		AllowAddDelete: true,
		SkipDisabled:   true,
	}
}

// TierRule constructs a score-tier rule matching scores >= minScore and resolving entryID.
func TierRule(id, label string, minScore float64, entryID string) RuleEntry {
	return RuleEntry{
		ID:       id,
		Label:    label,
		Enabled:  true,
		MinScore: minScore,
		EntryID:  entryID,
	}
}

// ConditionRule constructs a general field comparison rule.
func ConditionRule(id, label, field string, op RuleOp, value any, entryID string) RuleEntry {
	return RuleEntry{
		ID:      id,
		Label:   label,
		Enabled: true,
		Field:   field,
		Op:      op,
		Value:   value,
		EntryID: entryID,
	}
}

func (r RuleEntry) WithCatalog(catalogID, entryID string) RuleEntry {
	r.CatalogID = catalogID
	r.EntryID = entryID
	return r
}

func (r RuleEntry) WithMaxValue(maxVal float64) RuleEntry {
	r.MaxValue = &maxVal
	return r
}

func (r RuleEntry) WithOutput(out map[string]any) RuleEntry {
	r.Output = maps.Clone(out)
	return r
}

func (s RulesetSpec) WithHelp(h string) RulesetSpec               { s.Help = h; return s }
func (s RulesetSpec) WithCatalog(catalogID string) RulesetSpec    { s.CatalogID = catalogID; return s }
func (s RulesetSpec) WithOutputFields(f ...FormField) RulesetSpec { s.OutputFields = f; return s }
func (s RulesetSpec) WithAddDelete(allow bool) RulesetSpec        { s.AllowAddDelete = allow; return s }
func (s RulesetSpec) WithSkipDisabled(skip bool) RulesetSpec      { s.SkipDisabled = skip; return s }

func (s RulesetSpec) WithDefaults(rules ...RuleEntry) RulesetSpec {
	cp := CloneRules(rules)
	if s.Kind == RulesetScoreTiers {
		sortScoreTiersDesc(cp)
	}
	s.Defaults = cp
	return s
}

// NormalizeRules validates a candidate rule list against RulesetSpec (and optional known catalogs).
func (s RulesetSpec) NormalizeRules(rules []RuleEntry, current []RuleEntry) ([]RuleEntry, error) {
	seen := make(map[string]struct{}, len(rules))
	out := make([]RuleEntry, 0, len(rules))

	for i, raw := range rules {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			id = fmt.Sprintf("rule_%d", i+1)
		}
		if !entryIDRe.MatchString(id) {
			return nil, ErrValidation.New("invalid rule id %q", id).WithProperty(PropCode, "invalid_rule_id")
		}
		if _, dup := seen[id]; dup {
			return nil, ErrValidation.New("duplicate rule id %q", id).WithProperty(PropCode, "duplicate_rule_id")
		}
		seen[id] = struct{}{}

		if math.IsNaN(raw.MinScore) || math.IsInf(raw.MinScore, 0) {
			return nil, ErrValidation.New("rule %q min_score must be a finite number", id)
		}

		cleanOut := map[string]any{}
		if len(s.OutputFields) > 0 {
			validated, err := ValidateFields(s.OutputFields, raw.Output)
			if err != nil {
				return nil, err
			}
			cleanOut = validated
		} else if len(raw.Output) > 0 {
			cleanOut = maps.Clone(raw.Output)
		}

		catID := strings.TrimSpace(raw.CatalogID)
		if catID == "" {
			catID = s.CatalogID
		}

		entry := RuleEntry{
			ID:        id,
			Label:     strings.TrimSpace(raw.Label),
			Enabled:   raw.Enabled,
			MinScore:  raw.MinScore,
			Field:     strings.TrimSpace(raw.Field),
			Op:        raw.Op,
			Value:     raw.Value,
			CatalogID: catID,
			EntryID:   strings.TrimSpace(raw.EntryID),
			Output:    cleanOut,
		}
		if raw.MaxValue != nil {
			mv := *raw.MaxValue
			entry.MaxValue = &mv
		}
		out = append(out, entry)
	}

	if !s.AllowAddDelete && len(current) > 0 && len(out) != len(current) {
		return nil, ErrForbidden.New("adding or removing tiers is disabled for %s", s.Title)
	}

	if s.Kind == RulesetScoreTiers {
		sortScoreTiersDesc(out)
	}
	return out, nil
}

func sortScoreTiersDesc(rules []RuleEntry) {
	slices.SortStableFunc(rules, func(a, b RuleEntry) int {
		return cmp.Compare(b.MinScore, a.MinScore)
	})
}

// CloneRules deep-copies a slice of RuleEntry items.
func CloneRules(in []RuleEntry) []RuleEntry {
	out := make([]RuleEntry, len(in))
	for i, r := range in {
		cp := r
		if r.MaxValue != nil {
			mv := *r.MaxValue
			cp.MaxValue = &mv
		}
		if r.Output != nil {
			cp.Output = maps.Clone(r.Output)
		}
		out[i] = cp
	}
	return out
}

// EvaluateRuleset evaluates a RulesetSpec against a round context (e.g. {"score": 280, "reason": "completed"})
// and resolves any linked CatalogEntry metadata (title, image_id, fields).
func EvaluateRuleset(
	spec RulesetSpec,
	rules []RuleEntry,
	catalogSpecs map[string]CatalogSpec,
	catalogViews map[string]CatalogView,
	roundCtx map[string]any,
) *ResolvedRule {
	if len(rules) == 0 {
		return nil
	}

	lookupCatalogEntry := func(catID, entryID string) (*CatalogEntry, CatalogSpec, bool) {
		if catID == "" || entryID == "" {
			return nil, CatalogSpec{}, false
		}
		view, ok := catalogViews[catID]
		if !ok {
			return nil, CatalogSpec{}, false
		}
		cSpec := catalogSpecs[catID]
		for i := range view.Entries {
			if view.Entries[i].ID == entryID {
				return &view.Entries[i], cSpec, true
			}
		}
		return nil, cSpec, false
	}

	if spec.Kind == RulesetScoreTiers {
		active := make([]RuleEntry, 0, len(rules))
		for _, r := range rules {
			if r.Enabled {
				active = append(active, r)
			}
		}
		if len(active) == 0 {
			return nil
		}
		sortScoreTiersDesc(active)

		scoreVal, _ := toFloat(roundCtx["score"])
		for i, r := range active {
			if scoreVal < r.MinScore {
				continue
			}
			catID := r.CatalogID
			if catID == "" {
				catID = spec.CatalogID
			}
			var resolvedEntry *CatalogEntry
			var cSpec CatalogSpec
			if catID != "" && r.EntryID != "" {
				ent, cs, found := lookupCatalogEntry(catID, r.EntryID)
				if !found {
					if spec.SkipDisabled {
						continue
					}
				} else if !ent.Enabled && spec.SkipDisabled {
					continue
				} else {
					resolvedEntry = ent
					cSpec = cs
				}
			}

			var maxScore *float64
			if i > 0 {
				upper := active[i-1].MinScore - 1
				if upper < r.MinScore {
					upper = r.MinScore
				}
				maxScore = &upper
			}

			res := &ResolvedRule{
				RulesetID:  spec.ID,
				RuleID:     r.ID,
				RuleLabel:  r.Label,
				TierIndex:  i + 1,
				TotalTiers: len(active),
				MinScore:   r.MinScore,
				MaxScore:   maxScore,
				CatalogID:  catID,
				EntryID:    r.EntryID,
				Output:     maps.Clone(r.Output),
			}
			if resolvedEntry != nil {
				res.Title = cSpec.EntryTitle(*resolvedEntry)
				res.ImageID = cSpec.EntryImageID(*resolvedEntry)
				res.Entry = maps.Clone(resolvedEntry.Fields)
			} else if r.Label != "" {
				res.Title = r.Label
			}
			return res
		}
		return nil
	}

	// General condition rules evaluation (in declared order).
	activeCount := 0
	for _, r := range rules {
		if r.Enabled {
			activeCount++
		}
	}
	idx := 0
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		idx++
		fieldKey := r.Field
		if fieldKey == "" {
			fieldKey = "score"
		}
		actual := roundCtx[fieldKey]
		if !matchCondition(actual, r.Op, r.Value, r.MaxValue) {
			continue
		}
		catID := r.CatalogID
		if catID == "" {
			catID = spec.CatalogID
		}
		var resolvedEntry *CatalogEntry
		var cSpec CatalogSpec
		if catID != "" && r.EntryID != "" {
			ent, cs, found := lookupCatalogEntry(catID, r.EntryID)
			if !found || (!ent.Enabled && spec.SkipDisabled) {
				continue
			}
			resolvedEntry = ent
			cSpec = cs
		}
		res := &ResolvedRule{
			RulesetID:  spec.ID,
			RuleID:     r.ID,
			RuleLabel:  r.Label,
			TierIndex:  idx,
			TotalTiers: activeCount,
			CatalogID:  catID,
			EntryID:    r.EntryID,
			Output:     maps.Clone(r.Output),
		}
		if resolvedEntry != nil {
			res.Title = cSpec.EntryTitle(*resolvedEntry)
			res.ImageID = cSpec.EntryImageID(*resolvedEntry)
			res.Entry = maps.Clone(resolvedEntry.Fields)
		} else if r.Label != "" {
			res.Title = r.Label
		}
		return res
	}
	return nil
}

func matchCondition(actual any, op RuleOp, expected any, maxVal *float64) bool {
	switch op {
	case OpGte, "":
		a, ok1 := toFloat(actual)
		b, ok2 := toFloat(expected)
		return ok1 && ok2 && a >= b
	case OpLte:
		a, ok1 := toFloat(actual)
		b, ok2 := toFloat(expected)
		return ok1 && ok2 && a <= b
	case OpBetween:
		a, ok1 := toFloat(actual)
		b, ok2 := toFloat(expected)
		if !ok1 || !ok2 {
			return false
		}
		if maxVal != nil {
			return a >= b && a <= *maxVal
		}
		return a >= b
	case OpEq:
		if a, ok1 := toFloat(actual); ok1 {
			if b, ok2 := toFloat(expected); ok2 {
				return a == b
			}
		}
		return fmt.Sprint(actual) == fmt.Sprint(expected)
	default:
		return false
	}
}
