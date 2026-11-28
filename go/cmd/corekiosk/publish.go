package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckpack"
	"github.com/GecEnterprises/corekiosk/go/ckupdater"
)

func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	outPath := fs.String("out", "release.key", "Output path for Ed25519 release signing keypair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kf, err := ckupdater.GenerateReleaseKeyPair()
	if err != nil {
		return err
	}
	if err := ckupdater.SaveReleaseKeyFile(*outPath, kf); err != nil {
		return err
	}
	fmt.Printf("Generated Ed25519 release keypair:\n  file:       %s\n  key_id:     %s\n  public_key: %s\n", *outPath, kf.KeyID, kf.PublicKey)
	return nil
}

func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	configPath := fs.String("config", "kiosk.json", "Path to kiosk.json")
	rootFlag := fs.String("root", "", "Monorepo root directory")
	target := fs.String("target", "win64", "Target artifact: win64 | game | companion_web | android")
	version := fs.String("version", "", "Release version (defaults to kiosk.json version)")
	channel := fs.String("channel", "stable", "Release channel (e.g. stable, beta)")
	policy := fs.String("policy", string(ckupdater.PolicyWhenIdle), "Update policy: immediate | when_idle | next_restart")
	filePath := fs.String("file", "", "Path to artifact .zip or .apk to sign and publish")
	signKey := fs.String("sign-key", "release.key", "Path to Ed25519 release.key")
	endpointURL := fs.String("url", os.Getenv("CK_UPDATES_URL"), "Updates publish endpoint URL (e.g. https://<proj>.supabase.co/functions/v1/updates)")
	publishKey := fs.String("publish-key", os.Getenv("CK_PUBLISH_API_KEY"), "Publish API key (X-CK-Publish-Key)")
	supabaseURL := fs.String("supabase-url", os.Getenv("SUPABASE_URL"), "Optional Supabase project URL to upload artifact to Storage")
	serviceKey := fs.String("service-key", os.Getenv("SUPABASE_SERVICE_ROLE_KEY"), "Supabase service role key for Storage upload")
	bucket := fs.String("bucket", "ck-releases", "Supabase Storage bucket name")
	publicURL := fs.String("artifact-url", "", "Explicit download URL if hosted externally (GitHub Releases, R2, etc.)")
	notes := fs.String("notes", "", "Optional release notes")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rootDir, cfg, err := ckpack.LoadOrDetectConfig(*configPath, *rootFlag)
	if err != nil {
		return err
	}
	dotEnv := loadDotEnv(filepath.Join(rootDir, ".ckpublish.env"))

	*endpointURL = firstNonEmpty(*endpointURL, dotEnv["CK_UPDATES_URL"], cfg.Updates.URL)
	*publishKey = firstNonEmpty(*publishKey, dotEnv["CK_PUBLISH_API_KEY"])
	*supabaseURL = firstNonEmpty(*supabaseURL, dotEnv["SUPABASE_URL"], cfg.Updates.SupabaseURL)
	*serviceKey = firstNonEmpty(*serviceKey, dotEnv["SUPABASE_SERVICE_ROLE_KEY"])
	if *bucket == "ck-releases" {
		*bucket = firstNonEmpty(dotEnv["CK_RELEASES_BUCKET"], cfg.Updates.Bucket, *bucket)
	}
	if *signKey == "release.key" {
		*signKey = firstNonEmpty(dotEnv["CK_RELEASE_KEY_FILE"], *signKey)
	}
	if !filepath.IsAbs(*signKey) && !ckpack.FileExists(*signKey) {
		cand := filepath.Join(rootDir, *signKey)
		if ckpack.FileExists(cand) {
			*signKey = cand
		}
	}
	if *filePath == "" {
		*filePath = autoDetectDistArtifact(rootDir, cfg.Name, cfg.Version, *target)
	}

	if *filePath == "" {
		return fmt.Errorf("-file is required (or run `corekiosk pack` first so ./dist/<app>-v<ver>-<os>-<arch>-<timestamp>.zip is auto-detected)")
	}
	if *endpointURL == "" {
		return fmt.Errorf("-url (or CK_UPDATES_URL / `corekiosk supabase` wizard) is required")
	}
	ver := strings.TrimSpace(*version)
	if ver == "" {
		ver = cfg.Version
	}

	priv, _, _, err := ckupdater.LoadReleaseKeyFile(*signKey)
	if err != nil {
		return fmt.Errorf("load sign key: %w", err)
	}

	rawBytes, err := os.ReadFile(*filePath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(rawBytes)
	shaHex := hex.EncodeToString(sum[:])

	storagePath := fmt.Sprintf("%s/%s/%s/%s", cfg.Name, ver, *target, filepath.Base(*filePath))
	if *supabaseURL != "" && *serviceKey != "" {
		uploadURL := strings.TrimRight(*supabaseURL, "/") + "/storage/v1/object/" + *bucket + "/" + storagePath
		req, err := http.NewRequest(http.MethodPost, uploadURL, bytes.NewReader(rawBytes))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+*serviceKey)
		req.Header.Set("apikey", *serviceKey)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("x-upsert", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("upload to supabase storage: %w", err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("supabase storage upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
		fmt.Printf("[publish] uploaded to storage: %s/%s\n", *bucket, storagePath)
	}

	art := ckupdater.TargetArtifact{
		Target:        ckupdater.UpdateTarget(*target),
		Version:       ver,
		Policy:        ckupdater.UpdatePolicy(*policy),
		URL:           *publicURL,
		StoragePath:   storagePath,
		SHA256:        shaHex,
		SizeBytes:     int64(len(rawBytes)),
		PublishedAtMs: time.Now().UnixMilli(),
		Notes:         *notes,
	}
	ckupdater.SignArtifact(priv, cfg.Name, &art)

	payload, _ := json.Marshal(map[string]any{
		"app":      cfg.Name,
		"channel":  *channel,
		"artifact": art,
	})
	req, err := http.NewRequest(http.MethodPost, *endpointURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if *publishKey != "" {
		req.Header.Set("X-CK-Publish-Key", *publishKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("publish HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	fmt.Printf("[publish] published %s %s@%s (%s, key=%s, sha256=%s)\n", cfg.Name, art.Target, art.Version, art.Policy, art.KeyID, art.SHA256[:12])
	return nil
}

func autoDetectDistArtifact(rootDir, appName, version, target string) string {
	distDir := filepath.Join(rootDir, "dist")
	if target == "android" {
		cand := filepath.Join(distDir, fmt.Sprintf("%s-companion-release.apk", sanitizeName(appName)))
		if ckpack.FileExists(cand) {
			return cand
		}
		return ""
	}
	exact := filepath.Join(distDir, fmt.Sprintf("%s-%s-windows-amd64.zip", appName, version))
	if ckpack.FileExists(exact) {
		return exact
	}
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod int64
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".zip") {
			continue
		}
		if info, err := ent.Info(); err == nil && info.ModTime().UnixNano() > newestMod {
			newestMod = info.ModTime().UnixNano()
			newest = filepath.Join(distDir, ent.Name())
		}
	}
	return newest
}
