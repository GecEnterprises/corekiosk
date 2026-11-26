package ckserver

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/gin-gonic/gin"
)

func (h *Hub) initCatalogsAndRulesets(ctx context.Context) error {
	now := time.Now().UnixMilli()

	for _, c := range h.spec.Catalogs {
		catSpec := c
		rev, entries, updatedAt, found, err := h.store.LoadCatalog(ctx, catSpec.ID)
		if err != nil {
			return err
		}
		if !found {
			seedEntries, err := catSpec.NormalizeEntries(catSpec.Defaults, nil, now)
			if err != nil {
				return err
			}
			rev, err = h.store.SaveCatalog(ctx, catSpec.ID, -1, seedEntries, now)
			if err != nil {
				return err
			}
			entries = seedEntries
			updatedAt = now
		}
		h.catalogs[catSpec.ID] = ckframework.CatalogView{
			CatalogID:   catSpec.ID,
			Title:       catSpec.Title,
			Revision:    rev,
			Entries:     entries,
			UpdatedAtMs: updatedAt,
		}

		if len(catSpec.Defaults) > 0 {
			cid := catSpec.ID
			title := catSpec.Title
			h.RegisterActionHandler("catalog."+cid+".reset_defaults", func(ctx context.Context, _ map[string]any, op *OperatorClaims) (ckframework.ActionResult, error) {
				actor := "launcher"
				if op != nil {
					actor = op.Username
				}
				view, err := h.ResetCatalogDefaults(ctx, cid, actor)
				if err != nil {
					return ckframework.ActionResult{}, err
				}
				return ckframework.ActionResult{
					OK:      true,
					Action:  "catalog." + cid + ".reset_defaults",
					Message: "Reset " + title + " to defaults.",
					Data:    map[string]any{"revision": view.Revision, "count": len(view.Entries)},
				}, nil
			})
		}
	}

	for _, r := range h.spec.Rulesets {
		rSpec := r
		rev, rules, updatedAt, found, err := h.store.LoadRuleset(ctx, rSpec.ID)
		if err != nil {
			return err
		}
		if !found {
			seedRules, err := rSpec.NormalizeRules(rSpec.Defaults, nil)
			if err != nil {
				return err
			}
			rev, err = h.store.SaveRuleset(ctx, rSpec.ID, -1, seedRules, now)
			if err != nil {
				return err
			}
			rules = seedRules
			updatedAt = now
		}
		h.rulesets[rSpec.ID] = ckframework.RulesetView{
			RulesetID:   rSpec.ID,
			Title:       rSpec.Title,
			Revision:    rev,
			Rules:       rules,
			UpdatedAtMs: updatedAt,
		}

		if len(rSpec.Defaults) > 0 {
			rid := rSpec.ID
			title := rSpec.Title
			h.RegisterActionHandler("ruleset."+rid+".reset_defaults", func(ctx context.Context, _ map[string]any, op *OperatorClaims) (ckframework.ActionResult, error) {
				actor := "launcher"
				if op != nil {
					actor = op.Username
				}
				view, err := h.ResetRulesetDefaults(ctx, rid, actor)
				if err != nil {
					return ckframework.ActionResult{}, err
				}
				return ckframework.ActionResult{
					OK:      true,
					Action:  "ruleset." + rid + ".reset_defaults",
					Message: "Reset " + title + " to defaults.",
					Data:    map[string]any{"revision": view.Revision, "count": len(view.Rules)},
				}, nil
			})
		}
	}

	return nil
}

func (h *Hub) Catalogs() map[string]ckframework.CatalogView {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]ckframework.CatalogView, len(h.catalogs))
	for k, v := range h.catalogs {
		cp := v
		cp.Entries = ckframework.CloneCatalogEntries(v.Entries)
		out[k] = cp
	}
	return out
}

func (h *Hub) Catalog(id string) (ckframework.CatalogView, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	v, ok := h.catalogs[id]
	if !ok {
		return ckframework.CatalogView{}, false
	}
	v.Entries = ckframework.CloneCatalogEntries(v.Entries)
	return v, true
}

func (h *Hub) SaveCatalogEntries(ctx context.Context, catalogID string, expectedRev int, candidate []ckframework.CatalogEntry, actor string) (ckframework.CatalogView, error) {
	spec, ok := h.spec.CatalogByID(catalogID)
	if !ok {
		return ckframework.CatalogView{}, ckframework.ErrNotFound.New("unknown catalog %q", catalogID)
	}
	now := time.Now().UnixMilli()

	h.mu.Lock()
	curView := h.catalogs[catalogID]
	clean, err := spec.NormalizeEntries(candidate, curView.Entries, now)
	if err != nil {
		h.mu.Unlock()
		return ckframework.CatalogView{}, err
	}
	nextRev, err := h.store.SaveCatalog(ctx, catalogID, expectedRev, clean, now)
	if err != nil {
		h.mu.Unlock()
		return ckframework.CatalogView{}, err
	}
	view := ckframework.CatalogView{
		CatalogID:   spec.ID,
		Title:       spec.Title,
		Revision:    nextRev,
		Entries:     clean,
		UpdatedAtMs: now,
	}
	h.catalogs[catalogID] = view
	h.seq++
	game := h.game
	h.mu.Unlock()

	if h.outbox != nil {
		_, _ = h.outbox.Record(ctx, cktelemetry.Event{
			Category: "audit",
			Kind:     "catalog.updated",
			Severity: "info",
			Payload: map[string]any{
				"catalog_id": catalogID,
				"revision":   nextRev,
				"actor":      actor,
				"count":      len(clean),
			},
		})
	}

	viewCopy := view
	viewCopy.Entries = ckframework.CloneCatalogEntries(view.Entries)
	h.broadcastTablets(gin.H{
		"type":       "catalog_updated",
		"catalog_id": viewCopy.CatalogID,
		"catalog":    viewCopy,
	})
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:       "catalog",
			Catalog: &viewCopy,
		})
	}
	return viewCopy, nil
}

func (h *Hub) AddCatalogEntry(ctx context.Context, catalogID string, raw ckframework.CatalogEntry, actor string) (ckframework.CatalogView, ckframework.CatalogEntry, error) {
	spec, ok := h.spec.CatalogByID(catalogID)
	if !ok {
		return ckframework.CatalogView{}, ckframework.CatalogEntry{}, ckframework.ErrNotFound.New("unknown catalog %q", catalogID)
	}
	if !spec.AllowAddDelete {
		return ckframework.CatalogView{}, ckframework.CatalogEntry{}, ckframework.ErrForbidden.New("adding entries is disabled for %s", spec.Title)
	}
	if strings.TrimSpace(raw.ID) == "" {
		raw.ID = "item_" + strings.ToLower(cktelemetry.NewEventID()[16:])
	}
	cur, _ := h.Catalog(catalogID)
	next := append(cur.Entries, raw)
	view, err := h.SaveCatalogEntries(ctx, catalogID, -1, next, actor)
	if err != nil {
		return ckframework.CatalogView{}, ckframework.CatalogEntry{}, err
	}
	created := view.Entries[len(view.Entries)-1]
	return view, created, nil
}

type CatalogEntryPatch struct {
	Enabled *bool          `json:"enabled,omitempty"`
	Order   *int           `json:"order,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func (h *Hub) PatchCatalogEntry(ctx context.Context, catalogID, entryID string, patch CatalogEntryPatch, actor string) (ckframework.CatalogView, ckframework.CatalogEntry, error) {
	cur, ok := h.Catalog(catalogID)
	if !ok {
		return ckframework.CatalogView{}, ckframework.CatalogEntry{}, ckframework.ErrNotFound.New("unknown catalog %q", catalogID)
	}
	idx := -1
	for i, e := range cur.Entries {
		if e.ID == entryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ckframework.CatalogView{}, ckframework.CatalogEntry{}, ckframework.ErrNotFound.New("entry %q not found in %q", entryID, catalogID)
	}
	target := cur.Entries[idx]
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
	if patch.Fields != nil {
		if target.Fields == nil {
			target.Fields = map[string]any{}
		}
		for k, v := range patch.Fields {
			target.Fields[k] = v
		}
	}
	cur.Entries[idx] = target

	if patch.Order != nil {
		newPos := max(0, min(*patch.Order, len(cur.Entries)-1))
		if newPos != idx {
			without := append([]ckframework.CatalogEntry{}, cur.Entries[:idx]...)
			without = append(without, cur.Entries[idx+1:]...)
			reordered := make([]ckframework.CatalogEntry, 0, len(cur.Entries))
			reordered = append(reordered, without[:newPos]...)
			reordered = append(reordered, target)
			reordered = append(reordered, without[newPos:]...)
			cur.Entries = reordered
		}
	}

	view, err := h.SaveCatalogEntries(ctx, catalogID, -1, cur.Entries, actor)
	if err != nil {
		return ckframework.CatalogView{}, ckframework.CatalogEntry{}, err
	}
	var updated ckframework.CatalogEntry
	for _, e := range view.Entries {
		if e.ID == entryID {
			updated = e
			break
		}
	}
	return view, updated, nil
}

func (h *Hub) DeleteCatalogEntry(ctx context.Context, catalogID, entryID, actor string) (ckframework.CatalogView, error) {
	spec, ok := h.spec.CatalogByID(catalogID)
	if !ok {
		return ckframework.CatalogView{}, ckframework.ErrNotFound.New("unknown catalog %q", catalogID)
	}
	if !spec.AllowAddDelete {
		return ckframework.CatalogView{}, ckframework.ErrForbidden.New("deleting entries is disabled for %s", spec.Title)
	}
	cur, _ := h.Catalog(catalogID)
	next := make([]ckframework.CatalogEntry, 0, len(cur.Entries))
	found := false
	for _, e := range cur.Entries {
		if e.ID == entryID {
			found = true
			continue
		}
		next = append(next, e)
	}
	if !found {
		return ckframework.CatalogView{}, ckframework.ErrNotFound.New("entry %q not found in %q", entryID, catalogID)
	}
	return h.SaveCatalogEntries(ctx, catalogID, -1, next, actor)
}

func (h *Hub) ResetCatalogDefaults(ctx context.Context, catalogID, actor string) (ckframework.CatalogView, error) {
	spec, ok := h.spec.CatalogByID(catalogID)
	if !ok {
		return ckframework.CatalogView{}, ckframework.ErrNotFound.New("unknown catalog %q", catalogID)
	}
	return h.SaveCatalogEntries(ctx, catalogID, -1, ckframework.CloneCatalogEntries(spec.Defaults), actor)
}

func (h *Hub) Rulesets() map[string]ckframework.RulesetView {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]ckframework.RulesetView, len(h.rulesets))
	for k, v := range h.rulesets {
		cp := v
		cp.Rules = ckframework.CloneRules(v.Rules)
		out[k] = cp
	}
	return out
}

func (h *Hub) Ruleset(id string) (ckframework.RulesetView, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	v, ok := h.rulesets[id]
	if !ok {
		return ckframework.RulesetView{}, false
	}
	v.Rules = ckframework.CloneRules(v.Rules)
	return v, true
}

func (h *Hub) SaveRuleset(ctx context.Context, rulesetID string, expectedRev int, candidate []ckframework.RuleEntry, actor string) (ckframework.RulesetView, error) {
	spec, ok := h.spec.RulesetByID(rulesetID)
	if !ok {
		return ckframework.RulesetView{}, ckframework.ErrNotFound.New("unknown ruleset %q", rulesetID)
	}
	now := time.Now().UnixMilli()

	h.mu.Lock()
	curView := h.rulesets[rulesetID]
	clean, err := spec.NormalizeRules(candidate, curView.Rules)
	if err != nil {
		h.mu.Unlock()
		return ckframework.RulesetView{}, err
	}
	nextRev, err := h.store.SaveRuleset(ctx, rulesetID, expectedRev, clean, now)
	if err != nil {
		h.mu.Unlock()
		return ckframework.RulesetView{}, err
	}
	view := ckframework.RulesetView{
		RulesetID:   spec.ID,
		Title:       spec.Title,
		Revision:    nextRev,
		Rules:       clean,
		UpdatedAtMs: now,
	}
	h.rulesets[rulesetID] = view
	h.seq++
	game := h.game
	h.mu.Unlock()

	if h.outbox != nil {
		_, _ = h.outbox.Record(ctx, cktelemetry.Event{
			Category: "audit",
			Kind:     "ruleset.updated",
			Severity: "info",
			Payload: map[string]any{
				"ruleset_id": rulesetID,
				"revision":   nextRev,
				"actor":      actor,
				"count":      len(clean),
			},
		})
	}

	viewCopy := view
	viewCopy.Rules = ckframework.CloneRules(view.Rules)
	h.broadcastTablets(gin.H{
		"type":       "ruleset_updated",
		"ruleset_id": viewCopy.RulesetID,
		"ruleset":    viewCopy,
	})
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:       "ruleset",
			Ruleset: &viewCopy,
		})
	}
	return viewCopy, nil
}

func (h *Hub) ResetRulesetDefaults(ctx context.Context, rulesetID, actor string) (ckframework.RulesetView, error) {
	spec, ok := h.spec.RulesetByID(rulesetID)
	if !ok {
		return ckframework.RulesetView{}, ckframework.ErrNotFound.New("unknown ruleset %q", rulesetID)
	}
	return h.SaveRuleset(ctx, rulesetID, -1, ckframework.CloneRules(spec.Defaults), actor)
}

// EvaluateRoundOutcome runs all declared Rulesets against the finished round context and
// produces a structured Outcome map + per-ruleset ResolvedRule map.
func (h *Hub) EvaluateRoundOutcome(score int, reason string, fields map[string]any) (map[string]any, map[string]*ckframework.ResolvedRule) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	roundCtx := map[string]any{
		"score":  score,
		"reason": reason,
	}
	for k, v := range fields {
		if _, reserved := roundCtx[k]; !reserved {
			roundCtx[k] = v
		}
	}

	catSpecs := make(map[string]ckframework.CatalogSpec, len(h.spec.Catalogs))
	for _, cs := range h.spec.Catalogs {
		catSpecs[cs.ID] = cs
	}
	catViews := maps.Clone(h.catalogs)

	rewards := map[string]*ckframework.ResolvedRule{}
	var primary *ckframework.ResolvedRule
	for _, rSpec := range h.spec.Rulesets {
		rView, ok := h.rulesets[rSpec.ID]
		if !ok {
			continue
		}
		resolved := ckframework.EvaluateRuleset(rSpec, rView.Rules, catSpecs, catViews, roundCtx)
		if resolved != nil {
			rewards[rSpec.ID] = resolved
			if primary == nil {
				primary = resolved
			}
		}
	}

	outcome := map[string]any{
		"score":  score,
		"reason": reason,
	}
	if len(rewards) > 0 {
		outcome["rewards"] = rewards
	}
	if primary != nil {
		outcome["reward_id"] = primary.EntryID
		outcome["reward_title"] = primary.Title
		outcome["reward_image_id"] = primary.ImageID
		outcome["reward_tier"] = primary.TierIndex
		outcome["reward_total_tiers"] = primary.TotalTiers
	}
	return outcome, rewards
}
