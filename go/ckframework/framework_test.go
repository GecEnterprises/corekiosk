package ckframework

import (
	"net/http"
	"testing"
)

func TestFormDefaultsToTelemetryTransmitAndValidates(t *testing.T) {
	form := NewForm("registration", "Registration",
		TextField("name", "Name").RequiredField().MaxLength(24),
		HandleField("ig", "Instagram").WithPrefix("@").MaxLength(30),
		EmailField("email", "Email").RequiredField(),
	)
	if !form.TelemetryTransmit {
		t.Fatalf("expected TelemetryTransmit to default to true")
	}

	cleaned, err := ValidateFields(form.Fields, map[string]any{
		"name":  "  Alya ",
		"ig":    "@alya.dev",
		"email": "alya@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if cleaned["name"] != "Alya" || cleaned["ig"] != "alya.dev" {
		t.Fatalf("unexpected cleaned values: %#v", cleaned)
	}

	_, err = ValidateFields(form.Fields, map[string]any{"name": "", "email": "not-an-email"})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	status, code, _ := HTTPStatus(err)
	if status != http.StatusBadRequest || code != "invalid_form" {
		t.Fatalf("expected 400 invalid_form, got %d %s", status, code)
	}
}

func TestSettingsValidationAndRuntimeSplit(t *testing.T) {
	app := NewApp("test-kiosk", "1.0.0").
		WithSettings(
			EnumSetting("display.mode", "Display", "Mode", []EnumOption{
				{Value: "square", Label: "Square"},
				{Value: "centered", Label: "LED Centered"},
			}, "square"),
			BoolSetting("debug.hitboxes", "Display", "Hitboxes", false).RuntimeOnly(),
			ColorSetting("theme.accent", "Display", "Accent", "#FF3366"),
		).
		WithBoards(
			NewBoard("top_scores", "Leaderboard").WithBoardClear(true),
		)

	if _, ok := app.ActionByID("board.top_scores.clear"); !ok {
		t.Fatalf("expected auto-registered clear board action")
	}

	persisted, runtime := app.DefaultSettings()
	nextP, nextR, changed, persistedChanged, err := ValidateConfigPatch(app.Settings, persisted, runtime, map[string]any{
		"debug.hitboxes": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if persistedChanged {
		t.Fatalf("runtime-only patch should not mark persistedChanged")
	}
	if nextR["debug.hitboxes"] != true || len(changed) != 1 {
		t.Fatalf("unexpected runtime patch result: %v %v", nextR, changed)
	}
	if nextP["display.mode"] != "square" {
		t.Fatalf("persisted defaults mutated unexpectedly")
	}
}

func TestCatalogAndScoreTierRulesetEvaluation(t *testing.T) {
	// 1x1 transparent PNG
	tinyPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	seed := SeedAsset("raincoat", "raincoat.png", tinyPNG)
	if seed.MimeType != "image/png" || seed.ID == "" {
		t.Fatalf("expected valid PNG seed asset, got %+v", seed)
	}

	prizes := NewCatalog("prizes", "Prizes",
		TextField("name", "Name").RequiredField(),
		ImageField("image", "Image"),
	).WithDefaults(
		NewCatalogEntry("raincoat", map[string]any{"name": "Rain Coat", "image": seed.ID}),
		NewCatalogEntry("tshirt", map[string]any{"name": "T-Shirt", "image": seed.ID}),
		NewCatalogEntry("totebag", map[string]any{"name": "Sling Bag", "image": seed.ID}),
		NewCatalogEntry("scarf", map[string]any{"name": "Cap Scarf", "image": seed.ID}),
	)

	tiers := NewScoreTierRuleset("prize_tiers", "Prize Tiers", "prizes").WithDefaults(
		TierRule("tier_1", "Tier 1", 400, "raincoat"),
		TierRule("tier_2", "Tier 2", 250, "tshirt"),
		TierRule("tier_3", "Tier 3", 100, "totebag"),
		TierRule("tier_4", "Tier 4", 0, "scarf"),
	)

	entries, err := prizes.NormalizeEntries(prizes.Defaults, nil, 1000)
	if err != nil {
		t.Fatalf("normalize entries: %v", err)
	}
	catSpecs := map[string]CatalogSpec{"prizes": prizes}
	catViews := map[string]CatalogView{"prizes": {CatalogID: "prizes", Entries: entries}}

	// Score 280 -> Tier 2 (250..399) -> T-Shirt
	res := EvaluateRuleset(tiers, tiers.Defaults, catSpecs, catViews, map[string]any{"score": 280})
	if res == nil || res.EntryID != "tshirt" || res.Title != "T-Shirt" || res.TierIndex != 2 || res.MaxScore == nil || *res.MaxScore != 399 {
		t.Fatalf("unexpected resolved tier for 280: %+v", res)
	}

	// Disable Rain Coat (out of stock) -> Score 500 automatically cascades to Tier 2 (T-Shirt)
	entries[0].Enabled = false
	catViews["prizes"] = CatalogView{CatalogID: "prizes", Entries: entries}
	resFallback := EvaluateRuleset(tiers, tiers.Defaults, catSpecs, catViews, map[string]any{"score": 500})
	if resFallback == nil || resFallback.EntryID != "tshirt" {
		t.Fatalf("expected disabled top prize to fall back to tshirt, got %+v", resFallback)
	}
}
