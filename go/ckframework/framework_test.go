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
