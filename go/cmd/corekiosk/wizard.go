package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GecEnterprises/corekiosk/go/ckpack"
	"github.com/GecEnterprises/corekiosk/go/ckupdater"
	cksupabase "github.com/GecEnterprises/corekiosk/supabase"
	"github.com/charmbracelet/huh"
)

func runSupabaseWizard(args []string) error {
	if len(args) > 0 && (args[0] == "setup" || args[0] == "init" || args[0] == "wizard") {
		args = args[1:]
	}

	fs := flag.NewFlagSet("supabase", flag.ContinueOnError)
	configPath := fs.String("config", "kiosk.json", "Path to kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory")
	projectRef := fs.String("project-ref", "", "Supabase 20-char project reference ID")
	dbPassword := fs.String("db-password", "", "Optional Supabase Postgres password for `supabase link`")
	telemetryKey := fs.String("telemetry-key", "", "Shared CK_TELEMETRY_API_KEY secret")
	publishKey := fs.String("publish-key", "", "Secret CK_PUBLISH_API_KEY for `corekiosk publish`")
	serviceKey := fs.String("service-key", "", "Optional SUPABASE_SERVICE_ROLE_KEY for Storage bucket setup & artifact uploads")
	signKeyPath := fs.String("sign-key", "release.key", "Path to Ed25519 release signing keypair")
	channel := fs.String("channel", "stable", "Default release channel")
	bucket := fs.String("bucket", "ck-releases", "Supabase Storage bucket for release artifacts")
	deployNow := fs.Bool("deploy", true, "Run Supabase CLI link, db push, secrets set, and functions deploy")
	nonInteractive := fs.Bool("non-interactive", false, "Run without interactive TUI prompts (use flags/defaults)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rootDir, cfg, err := ckpack.LoadOrDetectConfig(*configPath, *rootFlag)
	if err != nil {
		return err
	}
	envFile := filepath.Join(rootDir, ".ckpublish.env")
	savedEnv := loadDotEnv(envFile)

	ref := strings.TrimSpace(*projectRef)
	if ref == "" {
		ref = detectSupabaseProjectRef(rootDir, cfg, savedEnv)
	}
	tKey := firstNonEmpty(*telemetryKey, savedEnv["CK_TELEMETRY_API_KEY"], os.Getenv("CK_TELEMETRY_API_KEY"), randomHexSecret(16))
	pKey := firstNonEmpty(*publishKey, savedEnv["CK_PUBLISH_API_KEY"], os.Getenv("CK_PUBLISH_API_KEY"), randomHexSecret(24))
	sKey := firstNonEmpty(*serviceKey, savedEnv["SUPABASE_SERVICE_ROLE_KEY"], os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
	dbPass := strings.TrimSpace(*dbPassword)
	ch := firstNonEmpty(*channel, cfg.Updates.Channel, "stable")
	bkt := firstNonEmpty(*bucket, cfg.Updates.Bucket, "ck-releases")
	keyFile := strings.TrimSpace(*signKeyPath)
	if keyFile == "" {
		keyFile = "release.key"
	}
	runCLI := *deployNow

	selectedFeatures := []string{"telemetry", "updates"}

	if !*nonInteractive {
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewNote().
					Title("corekiosk — Supabase & Auto-Updater Setup Wizard").
					Description(fmt.Sprintf("App: %s (v%s)\nRoot: %s\n\nThis wizard scaffolds ./supabase, generates your Ed25519 release signing keypair, updates kiosk.json, and optionally invokes the Supabase CLI to link, push SQL migrations, set secrets, and deploy Edge Functions.", cfg.Name, cfg.Version, rootDir)),
				huh.NewMultiSelect[string]().
					Title("Which infrastructure components do you want to provision?").
					Options(
						huh.NewOption("Telemetry & Leads Backend (PostgreSQL tables/views + `telemetry` Edge Function)", "telemetry").Selected(true),
						huh.NewOption("Auto-Updater & Release Signing (`releases` tables + `ck-releases` bucket + `updates` Edge Function)", "updates").Selected(true),
					).
					Value(&selectedFeatures).
					Validate(func(v []string) error {
						if len(v) == 0 {
							return fmt.Errorf("select at least one component")
						}
						return nil
					}),
				huh.NewInput().
					Title("Supabase Project Reference ID").
					Description("20-character project ref from https://supabase.com/dashboard/project/<project-ref>").
					Placeholder("e.g. fdicweebtgnheqrekahr").
					Value(&ref).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("project reference ID is required")
						}
						return nil
					}),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("CK_TELEMETRY_API_KEY (kiosk ingest & update polling key)").
					Description("Shared secret sent in X-CK-API-Key by kiosks & tablets (leave pre-filled random value or customize)").
					Value(&tKey),
				huh.NewInput().
					Title("CK_PUBLISH_API_KEY (release publisher key)").
					Description("Secret sent in X-CK-Publish-Key when running `corekiosk publish`").
					Value(&pKey),
				huh.NewInput().
					Title("Ed25519 Release Signing Keypair Path").
					Description("Will be generated automatically if it does not exist yet (keep private; public key is written to kiosk.json)").
					Value(&keyFile),
				huh.NewInput().
					Title("Default Update Channel").
					Description("Release channel kiosks poll by default (e.g. stable, beta)").
					Value(&ch),
				huh.NewInput().
					Title("Supabase Storage Bucket for Release Artifacts").
					Description("Private Storage bucket used by `corekiosk publish` and the `updates` Edge Function").
					Value(&bkt),
				huh.NewInput().
					Title("SUPABASE_SERVICE_ROLE_KEY (optional)").
					Description("Used to auto-create the private Storage bucket and saved to gitignored .ckpublish.env for 1-command `corekiosk publish`").
					EchoMode(huh.EchoModePassword).
					Value(&sKey),
			),
			huh.NewGroup(
				huh.NewConfirm().
					Title("Run Supabase CLI now to link project, push SQL migrations, set secrets, and deploy Edge Functions?").
					Description("Requires `supabase` (or `npx supabase`) logged in (`npx supabase login`)").
					Affirmative("Yes, deploy now").
					Negative("No, only scaffold files & config").
					Value(&runCLI),
				huh.NewInput().
					Title("Supabase Database Password (optional)").
					Description("Passed to `supabase link --password` if provided; leave blank if already linked or to prompt in CLI").
					EchoMode(huh.EchoModePassword).
					Value(&dbPass),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
	}

	enableTelemetry := hasSliceItem(selectedFeatures, "telemetry")
	enableUpdates := hasSliceItem(selectedFeatures, "updates")
	ref = strings.TrimSpace(ref)
	supabaseBaseURL := fmt.Sprintf("https://%s.supabase.co", ref)
	telemetryURL := supabaseBaseURL + "/functions/v1/telemetry"
	updatesURL := supabaseBaseURL + "/functions/v1/updates"

	// 1. Scaffold ./supabase directory from embedded canonical SQL migrations & Edge Functions.
	supabaseDir := filepath.Join(rootDir, "supabase")
	written, err := cksupabase.Scaffold(supabaseDir, enableTelemetry, enableUpdates)
	if err != nil {
		return fmt.Errorf("scaffold ./supabase: %w", err)
	}
	fmt.Printf("\n[wizard] scaffolded %d file(s) in %s\n", len(written), supabaseDir)

	// 2. Ensure Ed25519 release signing keypair exists if updates are enabled.
	var pubKeyBase64, keyID string
	absKeyPath := keyFile
	if !filepath.IsAbs(absKeyPath) {
		absKeyPath = filepath.Join(rootDir, keyFile)
	}
	if enableUpdates {
		if ckpack.FileExists(absKeyPath) {
			_, pub, id, err := ckupdater.LoadReleaseKeyFile(absKeyPath)
			if err != nil {
				return fmt.Errorf("load existing %s: %w", absKeyPath, err)
			}
			pubKeyBase64 = ckupdater.EncodePublicKeyBase64(pub)
			keyID = id
			fmt.Printf("[wizard] using existing Ed25519 release key: %s (key_id=%s)\n", absKeyPath, keyID)
		} else {
			kf, err := ckupdater.GenerateReleaseKeyPair()
			if err != nil {
				return fmt.Errorf("generate release keypair: %w", err)
			}
			if err := ckupdater.SaveReleaseKeyFile(absKeyPath, kf); err != nil {
				return fmt.Errorf("save release keypair: %w", err)
			}
			pubKeyBase64 = kf.PublicKey
			keyID = kf.KeyID
			fmt.Printf("[wizard] generated Ed25519 release keypair: %s (key_id=%s)\n", absKeyPath, keyID)
		}
	}

	// 3. Update kiosk.json with telemetry & updates configuration.
	if enableTelemetry {
		cfg.Telemetry.URL = telemetryURL
	}
	if enableUpdates {
		cfg.Updates.URL = updatesURL
		cfg.Updates.Channel = ch
		cfg.Updates.Bucket = bkt
		cfg.Updates.SupabaseURL = supabaseBaseURL
		cfg.Updates.TrustedKeys = mergeCSVKey(cfg.Updates.TrustedKeys, pubKeyBase64)
	}
	kioskJSONPath := filepath.Join(rootDir, "kiosk.json")
	if rawCfg, err := json.MarshalIndent(cfg, "", "  "); err == nil {
		if err := os.WriteFile(kioskJSONPath, append(rawCfg, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", kioskJSONPath, err)
		}
		fmt.Printf("[wizard] updated %s with Supabase endpoints & trusted release public key\n", kioskJSONPath)
	}

	// 4. Save local publisher credentials in .ckpublish.env and ensure .gitignore protects secrets.
	if err := writePublishEnv(envFile, map[string]string{
		"SUPABASE_PROJECT_REF":      ref,
		"SUPABASE_URL":              supabaseBaseURL,
		"SUPABASE_SERVICE_ROLE_KEY": strings.TrimSpace(sKey),
		"CK_TELEMETRY_URL":          telemetryURL,
		"CK_TELEMETRY_API_KEY":      strings.TrimSpace(tKey),
		"CK_UPDATES_URL":            updatesURL,
		"CK_PUBLISH_API_KEY":        strings.TrimSpace(pKey),
		"CK_RELEASES_BUCKET":        bkt,
		"CK_RELEASE_KEY_FILE":       keyFile,
	}); err == nil {
		fmt.Printf("[wizard] saved local publish/telemetry secrets to %s\n", envFile)
	}
	ensureGitignoreEntries(filepath.Join(rootDir, ".gitignore"), []string{
		"release.key",
		"*.release.key",
		".ckpublish.env",
	})

	// 5. Create private Supabase Storage bucket via REST API if service_role key was provided.
	if enableUpdates && strings.TrimSpace(sKey) != "" {
		if err := ensureSupabaseStorageBucket(supabaseBaseURL, strings.TrimSpace(sKey), bkt); err != nil {
			fmt.Printf("[wizard] note: storage bucket setup warning: %v\n", err)
		} else {
			fmt.Printf("[wizard] verified private Supabase Storage bucket %q\n", bkt)
		}
	}

	// 6. Execute Supabase CLI workflow if confirmed.
	if runCLI {
		if err := runSupabaseCLIWorkflow(rootDir, ref, dbPass, tKey, pKey, bkt, enableTelemetry, enableUpdates); err != nil {
			return err
		}
	}

	fmt.Printf(`
================================================================================
Supabase & ckupdater Setup Complete!
================================================================================
  Project Ref       : %s
  Telemetry URL     : %s
  Updates URL       : %s
  Release Key ID    : %s
  Trusted Public Key: %s

Next steps:
  1. Build a release bundle:
       corekiosk pack
  2. Sign, upload, and publish to Supabase (automatically reads .ckpublish.env & kiosk.json):
       corekiosk publish
================================================================================
`, ref, telemetryURL, updatesURL, keyID, pubKeyBase64)
	return nil
}

func runSupabaseCLIWorkflow(rootDir, projectRef, dbPassword, telemetryKey, publishKey, bucket string, enableTelemetry, enableUpdates bool) error {
	sbBin, sbPrefixArgs := resolveSupabaseCLI()
	runSB := func(desc string, args ...string) error {
		full := append(append([]string{}, sbPrefixArgs...), args...)
		fmt.Printf("\n[supabase] %s: %s %s\n", desc, sbBin, strings.Join(full, " "))
		return runStreaming(rootDir, nil, sbBin, full...)
	}

	linkArgs := []string{"link", "--project-ref", projectRef}
	if dbPassword != "" {
		linkArgs = append(linkArgs, "--password", dbPassword)
	}
	if err := runSB("linking Supabase project", linkArgs...); err != nil {
		return fmt.Errorf("supabase link failed (run `npx supabase login` first if unauthorized): %w", err)
	}

	dbPushArgs := []string{"db", "push", "--remote", "--yes", "--include-all"}
	if dbPassword != "" {
		dbPushArgs = append(dbPushArgs, "--password", dbPassword)
	}
	if err := runSB("pushing PostgreSQL migrations to remote Supabase project", dbPushArgs...); err != nil {
		return fmt.Errorf("supabase db push failed: %w", err)
	}

	var secrets []string
	if strings.TrimSpace(telemetryKey) != "" {
		secrets = append(secrets, "CK_TELEMETRY_API_KEY="+strings.TrimSpace(telemetryKey))
	}
	if enableUpdates {
		if strings.TrimSpace(publishKey) != "" {
			secrets = append(secrets, "CK_PUBLISH_API_KEY="+strings.TrimSpace(publishKey))
		}
		if strings.TrimSpace(bucket) != "" {
			secrets = append(secrets, "CK_RELEASES_BUCKET="+strings.TrimSpace(bucket))
		}
	}
	if len(secrets) > 0 {
		secretArgs := append([]string{"secrets", "set", "--yes"}, secrets...)
		if err := runSB("setting Edge Function secrets", secretArgs...); err != nil {
			return fmt.Errorf("supabase secrets set failed: %w", err)
		}
	}

	if enableTelemetry {
		if err := runSB("deploying telemetry Edge Function (server-side bundling, no Docker)", "functions", "deploy", "telemetry", "--no-verify-jwt", "--use-api", "--yes"); err != nil {
			return fmt.Errorf("deploy telemetry function failed: %w", err)
		}
	}
	if enableUpdates {
		if err := runSB("deploying updates Edge Function (server-side bundling, no Docker)", "functions", "deploy", "updates", "--no-verify-jwt", "--use-api", "--yes"); err != nil {
			return fmt.Errorf("deploy updates function failed: %w", err)
		}
	}
	return nil
}

func resolveSupabaseCLI() (string, []string) {
	if p, err := exec.LookPath("supabase"); err == nil {
		return p, nil
	}
	return ckpack.NpxBin(), []string{"--yes", "supabase"}
}

func ensureSupabaseStorageBucket(supabaseURL, serviceKey, bucket string) error {
	payload, _ := json.Marshal(map[string]any{
		"id":     bucket,
		"name":   bucket,
		"public": false,
	})
	endpoint := strings.TrimRight(supabaseURL, "/") + "/storage/v1/bucket"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+serviceKey)
	req.Header.Set("apikey", serviceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	_ = resp.Body.Close()
	// HTTP 200/201 created, or HTTP 409/400 "Duplicate" when bucket already exists.
	if resp.StatusCode < 300 || resp.StatusCode == http.StatusConflict || strings.Contains(strings.ToLower(string(body)), "already exists") || strings.Contains(strings.ToLower(string(body)), "duplicate") {
		return nil
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

var supabaseRefRegex = regexp.MustCompile(`https://([a-z0-9]{15,32})\.supabase\.co`)

func detectSupabaseProjectRef(rootDir string, cfg ckpack.KioskProjectConfig, savedEnv map[string]string) string {
	if ref := strings.TrimSpace(savedEnv["SUPABASE_PROJECT_REF"]); ref != "" {
		return ref
	}
	if raw, err := os.ReadFile(filepath.Join(rootDir, "supabase", ".temp", "project-ref")); err == nil {
		if ref := strings.TrimSpace(string(raw)); ref != "" {
			return ref
		}
	}
	for _, candidate := range []string{cfg.Updates.SupabaseURL, cfg.Updates.URL, cfg.Telemetry.URL} {
		if m := supabaseRefRegex.FindStringSubmatch(candidate); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

func randomHexSecret(numBytes int) string {
	b := make([]byte, numBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func hasSliceItem(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func mergeCSVKey(existingCSV, newKey string) string {
	newKey = strings.TrimSpace(newKey)
	if newKey == "" {
		return existingCSV
	}
	var parts []string
	for _, p := range strings.Split(existingCSV, ",") {
		p = strings.TrimSpace(p)
		if p != "" && p != newKey {
			parts = append(parts, p)
		}
	}
	parts = append(parts, newKey)
	return strings.Join(parts, ",")
}

func loadDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}

func writePublishEnv(path string, kv map[string]string) error {
	keys := []string{
		"SUPABASE_PROJECT_REF",
		"SUPABASE_URL",
		"SUPABASE_SERVICE_ROLE_KEY",
		"CK_TELEMETRY_URL",
		"CK_TELEMETRY_API_KEY",
		"CK_UPDATES_URL",
		"CK_PUBLISH_API_KEY",
		"CK_RELEASES_BUCKET",
		"CK_RELEASE_KEY_FILE",
	}
	var b strings.Builder
	b.WriteString("# Generated by `corekiosk supabase` — DO NOT COMMIT (contains publish & service_role secrets)\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("%s=%s\n", k, kv[k]))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func ensureGitignoreEntries(gitignorePath string, entries []string) {
	raw, _ := os.ReadFile(gitignorePath)
	existing := string(raw)
	var toAdd []string
	for _, ent := range entries {
		found := false
		for _, line := range strings.Split(existing, "\n") {
			if strings.TrimSpace(line) == ent {
				found = true
				break
			}
		}
		if !found {
			toAdd = append(toAdd, ent)
		}
	}
	if len(toAdd) == 0 {
		return
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	existing += "\n# corekiosk release signing & publish secrets\n" + strings.Join(toAdd, "\n") + "\n"
	_ = os.WriteFile(gitignorePath, []byte(existing), 0o644)
}
