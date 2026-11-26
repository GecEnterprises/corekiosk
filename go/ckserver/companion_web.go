package ckserver

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

// NewCompanionWebHandler serves an exported React Native Web / Expo static directory
// with immutable caching for hashed `_expo/` bundles and SPA fallback to `index.html`.
func NewCompanionWebHandler(rootDir string) (http.Handler, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, ckframework.ErrValidation.Wrap(err, "resolve companion web directory")
	}
	indexFile := filepath.Join(absRoot, "index.html")
	info, err := os.Stat(indexFile)
	if err != nil || info.IsDir() {
		return nil, ckframework.ErrNotFound.New("companion web index.html not found in %s", absRoot)
	}

	fs := http.FileServer(http.Dir(absRoot))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		cleanPath := path.Clean("/" + r.URL.Path)
		relPath := strings.TrimPrefix(cleanPath, "/")
		if relPath == "" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, indexFile)
			return
		}

		diskPath := filepath.Join(absRoot, filepath.FromSlash(relPath))
		// Prevent path traversal outside absRoot.
		if relCheck, err := filepath.Rel(absRoot, diskPath); err != nil || strings.HasPrefix(relCheck, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if st, err := os.Stat(diskPath); err == nil && !st.IsDir() {
			if strings.HasPrefix(relPath, "_expo/static/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if relPath == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fs.ServeHTTP(w, r)
			return
		}

		// Missing asset files (.js, .css, .png, .ttf, .map) return 404; SPA routes get index.html.
		if ext := path.Ext(relPath); ext != "" && ext != ".html" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexFile)
	}), nil
}
