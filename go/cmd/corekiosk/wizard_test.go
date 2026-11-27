package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GecEnterprises/corekiosk/go/ckpack"
)

func TestSupabaseWizardNonInteractiveScaffoldsAndUpdatesConfig(t *testing.T) {
	tmp := t.TempDir()
	kioskPath := filepath.Join(tmp, "kiosk.json")
	initial := `{
  "name": "test-kiosk",
  "version": "0.1.0",
  "server": { "dir": "./server", "exe": "test-kiosk.exe" }
}`
	if err := os.WriteFile(kioskPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runSupabaseWizard([]string{
		"-config", kioskPath,
		"-root", tmp,
		"-project-ref", "abcdefghijklmnopqrst",
		"-telemetry-key", "test-tele-key",
		"-publish-key", "test-pub-key",
		"-channel", "beta",
		"-bucket", "ck-releases",
		"-deploy=false",
		"-non-interactive",
	})
	if err != nil {
		t.Fatalf("runSupabaseWizard failed: %v", err)
	}

	// Verify scaffolded supabase files
	for _, rel := range []string{
		"supabase/config.toml",
		"supabase/migrations/20250301000000_corekiosk_telemetry.sql",
		"supabase/migrations/20250302000000_corekiosk_updates.sql",
		"supabase/functions/telemetry/index.ts",
		"supabase/functions/updates/index.ts",
		"release.key",
		".ckpublish.env",
		".gitignore",
	} {
		if !ckpack.FileExists(filepath.Join(tmp, filepath.FromSlash(rel))) {
			t.Fatalf("expected file %s to be created", rel)
		}
	}

	_, cfg, err := ckpack.LoadOrDetectConfig(kioskPath, tmp)
	if err != nil {
		t.Fatalf("reload kiosk.json: %v", err)
	}
	if cfg.Updates.URL != "https://abcdefghijklmnopqrst.supabase.co/functions/v1/updates" {
		t.Fatalf("unexpected updates.url: %s", cfg.Updates.URL)
	}
	if cfg.Telemetry.URL != "https://abcdefghijklmnopqrst.supabase.co/functions/v1/telemetry" {
		t.Fatalf("unexpected telemetry.url: %s", cfg.Telemetry.URL)
	}
	if cfg.Updates.Channel != "beta" {
		t.Fatalf("unexpected updates.channel: %s", cfg.Updates.Channel)
	}
	if strings.TrimSpace(cfg.Updates.TrustedKeys) == "" {
		t.Fatalf("expected updates.trusted_keys to be populated with generated public key")
	}

	envMap := loadDotEnv(filepath.Join(tmp, ".ckpublish.env"))
	if envMap["CK_PUBLISH_API_KEY"] != "test-pub-key" || envMap["CK_TELEMETRY_API_KEY"] != "test-tele-key" {
		t.Fatalf("unexpected .ckpublish.env contents: %#v", envMap)
	}
}
