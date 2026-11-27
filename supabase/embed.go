package supabase

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Assets embeds the canonical Supabase config.toml, PostgreSQL migrations, and Edge Functions
// so `corekiosk supabase` can scaffold and deploy them from any activation project.
//
//go:embed config.toml migrations/*.sql functions/telemetry/index.ts functions/updates/index.ts
var Assets embed.FS

// Scaffold extracts the embedded Supabase configuration, SQL migrations, and Edge Functions
// into targetSupabaseDir (typically `<project-root>/supabase`).
func Scaffold(targetSupabaseDir string, enableTelemetry, enableUpdates bool) ([]string, error) {
	var written []string
	err := fs.WalkDir(Assets, ".", func(relPath string, d fs.DirEntry, err error) error {
		if err != nil || relPath == "." || d.IsDir() {
			return err
		}
		slash := filepath.ToSlash(relPath)
		if !enableTelemetry && (strings.Contains(slash, "telemetry.sql") || strings.HasPrefix(slash, "functions/telemetry/")) {
			return nil
		}
		if !enableUpdates && (strings.Contains(slash, "updates.sql") || strings.HasPrefix(slash, "functions/updates/")) {
			return nil
		}
		raw, err := Assets.ReadFile(relPath)
		if err != nil {
			return err
		}
		dst := filepath.Join(targetSupabaseDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
		written = append(written, dst)
		return nil
	})
	return written, err
}
