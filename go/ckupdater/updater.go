package ckupdater

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status reports the live state of the in-process updater for cklauncher UI / telemetry.
type Status struct {
	State          string       `json:"state"` // idle | checking | downloading | staged | applying | error
	Channel        string       `json:"channel"`
	PendingTarget  UpdateTarget `json:"pending_target,omitempty"`
	PendingVersion string       `json:"pending_version,omitempty"`
	Policy         UpdatePolicy `json:"policy,omitempty"`
	LastCheckedMs  int64        `json:"last_checked_ms,omitempty"`
	LastError      string       `json:"last_error,omitempty"`
}

// Config configures the in-process ckupdater Manager owned by cklauncher.
type Config struct {
	EndpointURL    string
	APIKey         string
	AppName        string
	Channel        string
	InstallationID string
	TrustedKeys    map[string]ed25519.PublicKey
	BaseDir        string
	HealthURL      string
	PollInterval   time.Duration

	// CurrentTargetSHA returns the local SHA-256 (or version) for a target so unchanged targets are skipped.
	CurrentTargetVersion func(target UpdateTarget) (version string, sha256Hex string)
	// IsIdle returns true when no active player round is in progress (required for PolicyWhenIdle).
	IsIdle func() bool
	// StopGame cleanly terminates the supervised child game before swapping files.
	StopGame func()
	// RestartGame restarts the supervised child game after a game-only or companion_web-only swap.
	RestartGame func()
	// OnExitForSelfUpdate is called right before cklauncher exits for a win64 self-update handoff.
	OnExitForSelfUpdate func()
	// OnStatus is called whenever updater Status changes.
	OnStatus func(Status)
}

// Manager runs inside cklauncher's process tree so it never leaks orphaned background daemons.
type Manager struct {
	cfg    Config
	http   *http.Client
	mu     sync.Mutex
	status Status
	staged map[UpdateTarget]stagedUpdate
}

type stagedUpdate struct {
	Artifact  TargetArtifact `json:"artifact"`
	StagedDir string         `json:"staged_dir"`
}

func NewManager(cfg Config) *Manager {
	if cfg.Channel == "" {
		cfg.Channel = "stable"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 15 * time.Minute
	}
	if cfg.BaseDir == "" {
		cfg.BaseDir = "."
	}
	return &Manager{
		cfg:    cfg,
		http:   &http.Client{Timeout: 60 * time.Second},
		status: Status{State: "idle", Channel: cfg.Channel},
		staged: make(map[UpdateTarget]stagedUpdate),
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) setStatus(fn func(*Status)) {
	m.mu.Lock()
	fn(&m.status)
	st := m.status
	m.mu.Unlock()
	if m.cfg.OnStatus != nil {
		m.cfg.OnStatus(st)
	}
}

// ApplyPendingOnBoot applies any PolicyNextRestart updates staged under ./updates/pending/ before the game starts.
func (m *Manager) ApplyPendingOnBoot() error {
	pendingRoot := filepath.Join(m.cfg.BaseDir, "updates", "pending")
	entries, err := os.ReadDir(pendingRoot)
	if err != nil {
		return nil
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		metaPath := filepath.Join(pendingRoot, ent.Name(), "artifact.json")
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var art TargetArtifact
		if json.Unmarshal(raw, &art) != nil {
			continue
		}
		if err := VerifyArtifact(m.cfg.TrustedKeys, m.cfg.AppName, art); err != nil {
			continue
		}
		stagedDir := filepath.Join(pendingRoot, ent.Name(), "files")
		if art.Target == TargetGame || art.Target == TargetCompanionWeb {
			if err := swapDirectory(stagedDir, m.cfg.BaseDir, filepath.Join(m.cfg.BaseDir, "updates", "backup", ent.Name())); err == nil {
				_ = os.RemoveAll(filepath.Join(pendingRoot, ent.Name()))
			}
		} else if art.Target == TargetWin64 {
			if err := m.launchSelfUpdateHandoff(stagedDir, art); err == nil {
				os.Exit(0)
			}
		}
	}
	return nil
}

// Run polls the update endpoint until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	if strings.TrimSpace(m.cfg.EndpointURL) == "" {
		return
	}
	_ = m.CheckAndHandle(ctx)
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.CheckAndHandle(ctx)
		}
	}
}

// CheckAndHandle checks for updates, stages & verifies any changed targets, and enforces the server's UpdatePolicy.
func (m *Manager) CheckAndHandle(ctx context.Context) error {
	if strings.TrimSpace(m.cfg.EndpointURL) == "" {
		return nil
	}
	m.setStatus(func(s *Status) {
		s.State = "checking"
		s.LastError = ""
	})

	manifest, err := m.fetchLatest(ctx)
	if err != nil {
		m.setStatus(func(s *Status) {
			s.State = "error"
			s.LastCheckedMs = time.Now().UnixMilli()
			s.LastError = err.Error()
		})
		return err
	}

	m.setStatus(func(s *Status) {
		s.LastCheckedMs = time.Now().UnixMilli()
	})

	// Check win64 first, then game, then companion_web.
	order := []UpdateTarget{TargetWin64, TargetGame, TargetCompanionWeb}
	for _, tgt := range order {
		art, ok := manifest.Targets[string(tgt)]
		if !ok || art.URL == "" {
			continue
		}
		if m.cfg.CurrentTargetVersion != nil {
			curVer, curSHA := m.cfg.CurrentTargetVersion(tgt)
			if curVer == art.Version || (curSHA != "" && strings.EqualFold(curSHA, art.SHA256)) {
				continue
			}
		}
		if err := VerifyArtifact(m.cfg.TrustedKeys, m.cfg.AppName, art); err != nil {
			m.setStatus(func(s *Status) {
				s.State = "error"
				s.LastError = err.Error()
			})
			return err
		}

		stagedDir, err := m.downloadAndStage(ctx, art)
		if err != nil {
			m.setStatus(func(s *Status) {
				s.State = "error"
				s.LastError = err.Error()
			})
			return err
		}

		policy := art.Policy
		if policy == "" {
			policy = PolicyWhenIdle
		}
		m.setStatus(func(s *Status) {
			s.State = "staged"
			s.PendingTarget = art.Target
			s.PendingVersion = art.Version
			s.Policy = policy
		})

		switch policy {
		case PolicyNextRestart:
			if err := m.promoteToPending(art, stagedDir); err != nil {
				return err
			}
		case PolicyWhenIdle:
			if err := m.waitUntilIdle(ctx); err != nil {
				return err
			}
			return m.applyStaged(art, stagedDir)
		case PolicyImmediate:
			return m.applyStaged(art, stagedDir)
		}
		return nil
	}

	m.setStatus(func(s *Status) {
		if s.State == "checking" {
			s.State = "idle"
		}
	})
	return nil
}

func (m *Manager) fetchLatest(ctx context.Context) (LatestUpdatesResponse, error) {
	u, err := url.Parse(m.cfg.EndpointURL)
	if err != nil {
		return LatestUpdatesResponse{}, err
	}
	q := u.Query()
	q.Set("app", m.cfg.AppName)
	q.Set("channel", m.cfg.Channel)
	if m.cfg.InstallationID != "" {
		q.Set("installation_id", m.cfg.InstallationID)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return LatestUpdatesResponse{}, err
	}
	if m.cfg.APIKey != "" {
		req.Header.Set("X-CK-API-Key", m.cfg.APIKey)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return LatestUpdatesResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return LatestUpdatesResponse{}, fmt.Errorf("updates endpoint HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out LatestUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return LatestUpdatesResponse{}, err
	}
	return out, nil
}

func (m *Manager) downloadAndStage(ctx context.Context, art TargetArtifact) (string, error) {
	m.setStatus(func(s *Status) {
		s.State = "downloading"
		s.PendingTarget = art.Target
		s.PendingVersion = art.Version
	})

	stageRoot := filepath.Join(m.cfg.BaseDir, "updates", "staged", string(art.Target)+"-"+art.Version)
	_ = os.RemoveAll(stageRoot)
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(stageRoot, "bundle.zip")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, art.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("download %s HTTP %d", art.Target, resp.StatusCode)
	}

	f, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}

	gotSHA := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(gotSHA, strings.TrimSpace(art.SHA256)) {
		_ = os.RemoveAll(stageRoot)
		return "", fmt.Errorf("sha256 mismatch for %s@%s: got %s want %s", art.Target, art.Version, gotSHA, art.SHA256)
	}

	filesDir := filepath.Join(stageRoot, "files")
	if err := unzipSafe(zipPath, filesDir); err != nil {
		return "", err
	}
	return filesDir, nil
}

func (m *Manager) promoteToPending(art TargetArtifact, stagedFilesDir string) error {
	pendingDir := filepath.Join(m.cfg.BaseDir, "updates", "pending", string(art.Target))
	_ = os.RemoveAll(pendingDir)
	if err := os.MkdirAll(pendingDir, 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(art, "", "  ")
	if err := os.WriteFile(filepath.Join(pendingDir, "artifact.json"), raw, 0o644); err != nil {
		return err
	}
	return os.Rename(stagedFilesDir, filepath.Join(pendingDir, "files"))
}

func (m *Manager) waitUntilIdle(ctx context.Context) error {
	if m.cfg.IsIdle == nil || m.cfg.IsIdle() {
		return nil
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if m.cfg.IsIdle() {
				return nil
			}
		}
	}
}

func (m *Manager) applyStaged(art TargetArtifact, stagedFilesDir string) error {
	m.setStatus(func(s *Status) {
		s.State = "applying"
	})
	if m.cfg.StopGame != nil {
		m.cfg.StopGame()
	}

	if art.Target == TargetGame || art.Target == TargetCompanionWeb {
		backupDir := filepath.Join(m.cfg.BaseDir, "updates", "backup", string(art.Target)+"-"+art.Version)
		if err := swapDirectory(stagedFilesDir, m.cfg.BaseDir, backupDir); err != nil {
			return err
		}
		if m.cfg.RestartGame != nil {
			m.cfg.RestartGame()
		}
		m.setStatus(func(s *Status) {
			s.State = "idle"
			s.PendingTarget = ""
			s.PendingVersion = ""
		})
		return nil
	}

	if err := m.launchSelfUpdateHandoff(stagedFilesDir, art); err != nil {
		return err
	}
	if m.cfg.OnExitForSelfUpdate != nil {
		m.cfg.OnExitForSelfUpdate()
	}
	os.Exit(0)
	return nil
}

func (m *Manager) launchSelfUpdateHandoff(stagedFilesDir string, art TargetArtifact) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	helperPath := filepath.Join(m.cfg.BaseDir, "updates", "ckupdater-helper"+filepath.Ext(exe))
	if err := os.MkdirAll(filepath.Dir(helperPath), 0o755); err != nil {
		return err
	}
	if err := copyFile(exe, helperPath); err != nil {
		return err
	}
	healthURL := m.cfg.HealthURL
	if healthURL == "" {
		healthURL = "http://127.0.0.1:8080/v1/health"
	}
	cmd := exec.Command(helperPath,
		"--apply-update",
		"--pid", strconv.Itoa(os.Getpid()),
		"--staged", stagedFilesDir,
		"--target", m.cfg.BaseDir,
		"--exe", exe,
		"--health", healthURL,
		"--version", art.Version,
	)
	return cmd.Start()
}

// HandleApplyUpdateFlag checks os.Args for `--apply-update` and executes the atomic swap + health-check rollback.
// Returns (true, err) if `--apply-update` was handled (caller should exit), or (false, nil) for normal boot.
func HandleApplyUpdateFlag(args []string) (bool, error) {
	isApply := false
	vals := map[string]string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--apply-update" {
			isApply = true
			continue
		}
		if strings.HasPrefix(args[i], "--") && i+1 < len(args) {
			vals[strings.TrimPrefix(args[i], "--")] = args[i+1]
			i++
		}
	}
	if !isApply {
		return false, nil
	}

	pid, _ := strconv.Atoi(vals["pid"])
	waitForPIDExit(pid, 15*time.Second)

	stagedDir := vals["staged"]
	targetDir := vals["target"]
	exePath := vals["exe"]
	healthURL := vals["health"]
	ver := vals["version"]
	backupDir := filepath.Join(targetDir, "updates", "backup", "pre-"+ver)

	if err := swapDirectory(stagedDir, targetDir, backupDir); err != nil {
		return true, err
	}

	child := exec.Command(exePath, "--now")
	child.Dir = targetDir
	if err := child.Start(); err != nil {
		_ = restoreDirectory(backupDir, targetDir)
		_ = exec.Command(exePath, "--now").Start()
		return true, err
	}

	if !waitHealthOK(healthURL, 15*time.Second) {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		_ = restoreDirectory(backupDir, targetDir)
		_ = exec.Command(exePath, "--now").Start()
		return true, fmt.Errorf("new version failed health check; rolled back")
	}
	return true, nil
}

func waitForPIDExit(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p, err := os.FindProcess(pid)
		if err != nil || p == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func waitHealthOK(url string, timeout time.Duration) bool {
	if url == "" {
		return true
	}
	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// swapDirectory copies staged files into targetDir while backing up overwritten files and preserving ./data/ and ./updates/.
func swapDirectory(stagedDir, targetDir, backupDir string) error {
	_ = os.RemoveAll(backupDir)
	return filepath.Walk(stagedDir, func(src string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stagedDir, src)
		if err != nil || rel == "." {
			return err
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		if top == "data" || top == "updates" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(targetDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if _, statErr := os.Stat(dst); statErr == nil {
			bak := filepath.Join(backupDir, rel)
			if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
				return err
			}
			_ = copyFile(dst, bak)
		}
		return copyFile(src, dst)
	})
}

func restoreDirectory(backupDir, targetDir string) error {
	if _, err := os.Stat(backupDir); err != nil {
		return err
	}
	return filepath.Walk(backupDir, func(src string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(backupDir, src)
		if err != nil || rel == "." {
			return err
		}
		dst := filepath.Join(targetDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		return copyFile(src, dst)
	})
}

func unzipSafe(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanDest) && filepath.Clean(target) != filepath.Clean(destDir) {
			return fmt.Errorf("illegal zip path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		_ = rc.Close()
		_ = out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
