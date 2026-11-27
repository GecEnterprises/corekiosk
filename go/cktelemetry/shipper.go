package cktelemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

// ShipperConfig configures the background HTTPS cloud telemetry uploader.
type ShipperConfig struct {
	IngestURL      string
	InstallationID string
	AppName        string
	DeviceID       string
	PrivateKey     ed25519.PrivateKey
	APIKey         string
	FlushInterval  time.Duration
	BatchSize      int
	HTTPClient     *http.Client
}

// TestResult reports the outcome of an active TestConnection probe against a telemetry ingest endpoint.
type TestResult struct {
	IngestURL   string `json:"ingest_url"`
	StatusCode  int    `json:"status_code"`
	LatencyMs   int64  `json:"latency_ms"`
	AcceptedIDs int    `json:"accepted_ids"`
	ServerID    string `json:"server_id"`
	Message     string `json:"message"`
}

// Shipper flushes unsynced rows from a local Outbox to any configured HTTPS telemetry
// ingest endpoint with Ed25519 signatures and exponential backoff.
type Shipper struct {
	outbox      *Outbox
	mu          sync.RWMutex
	cfg         ShipperConfig
	client      *http.Client
	cloudOnline atomic.Bool
	lastErr     atomic.Value
	lastTest    atomic.Value
	triggerCh   chan chan error
	wakeCh      chan struct{}
}

func NewShipper(outbox *Outbox, cfg ShipperConfig) *Shipper {
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 15 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	s := &Shipper{
		outbox:    outbox,
		cfg:       cfg,
		client:    client,
		triggerCh: make(chan chan error, 4),
		wakeCh:    make(chan struct{}, 1),
	}
	s.lastErr.Store("")
	s.lastTest.Store("")
	return s
}

// Config returns a thread-safe snapshot of the current ShipperConfig.
func (s *Shipper) Config() ShipperConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// UpdateConfig hot-reloads the telemetry ingest endpoint, API key, flush interval, and batch size.
func (s *Shipper) UpdateConfig(ingestURL, apiKey string, flushInterval time.Duration, batchSize int) {
	s.mu.Lock()
	s.cfg.IngestURL = strings.TrimSpace(ingestURL)
	s.cfg.APIKey = strings.TrimSpace(apiKey)
	if flushInterval > 0 {
		s.cfg.FlushInterval = flushInterval
	}
	if batchSize > 0 {
		s.cfg.BatchSize = batchSize
	}
	emptyURL := s.cfg.IngestURL == ""
	s.mu.Unlock()

	if emptyURL {
		s.cloudOnline.Store(false)
		s.lastErr.Store("")
	}
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *Shipper) CloudOnline() bool { return s.cloudOnline.Load() }

func (s *Shipper) LastError() string {
	v, _ := s.lastErr.Load().(string)
	return v
}

func (s *Shipper) LastTestResult() string {
	v, _ := s.lastTest.Load().(string)
	return v
}

// TestConnection sends a signed, gzipped `telemetry.test_ping` diagnostic batch to verify
// network reachability, API key validity, Ed25519 signature verification, and D1 database writes.
func (s *Shipper) TestConnection(ctx context.Context, overrideURL, overrideAPIKey string) (TestResult, error) {
	cfg := s.Config()
	if u := strings.TrimSpace(overrideURL); u != "" {
		cfg.IngestURL = u
	}
	if overrideAPIKey != "" || strings.TrimSpace(overrideURL) != "" {
		cfg.APIKey = strings.TrimSpace(overrideAPIKey)
	}
	if cfg.IngestURL == "" {
		return TestResult{}, ckframework.ErrValidation.New("telemetry ingest URL is empty")
	}
	parsedURL, err := url.Parse(cfg.IngestURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return TestResult{}, ckframework.ErrValidation.New("invalid telemetry ingest URL: must start with http:// or https://")
	}

	nowMs := time.Now().UnixMilli()
	pingEvent := Event{
		EventID:        NewEventID(),
		OriginDeviceID: cfg.DeviceID,
		OriginRole:     "server",
		OriginBootID:   "test",
		OriginSeq:      1,
		Category:       "system",
		Kind:           "telemetry.test_ping",
		Severity:       "info",
		OccurredAtMs:   nowMs,
		Payload: map[string]any{
			"source":       "cklauncher",
			"app_name":     cfg.AppName,
			"tested_at_ms": nowMs,
		},
	}

	start := time.Now()
	accepted, statusCode, err := s.uploadBatchWithConfig(ctx, cfg, []Event{pingEvent})
	latencyMs := max(1, time.Since(start).Milliseconds())
	res := TestResult{
		IngestURL:   cfg.IngestURL,
		StatusCode:  statusCode,
		LatencyMs:   latencyMs,
		AcceptedIDs: len(accepted),
		ServerID:    cfg.DeviceID,
	}
	if err != nil {
		s.cloudOnline.Store(false)
		s.lastErr.Store(err.Error())
		s.lastTest.Store(fmt.Sprintf("Failed (%dms): %s", latencyMs, err.Error()))
		res.Message = err.Error()
		return res, err
	}
	s.cloudOnline.Store(true)
	s.lastErr.Store("")
	res.Message = fmt.Sprintf("OK — HTTP %d (%dms, %d event accepted)", statusCode, latencyMs, len(accepted))
	s.lastTest.Store(res.Message)
	return res, nil
}

// FlushNow triggers an immediate sync attempt (used by cklauncher "Sync Now").
func (s *Shipper) FlushNow(ctx context.Context) (int, error) {
	cfg := s.Config()
	if cfg.IngestURL == "" {
		s.cloudOnline.Store(false)
		return 0, nil
	}
	total := 0
	var cursor int64
	for {
		events, maxSeq, hasMore, err := s.outbox.PullUnsynced(ctx, cursor, cfg.BatchSize)
		if err != nil {
			return total, err
		}
		if len(events) == 0 {
			s.cloudOnline.Store(true)
			s.lastErr.Store("")
			return total, nil
		}
		accepted, _, err := s.uploadBatchWithConfig(ctx, cfg, events)
		if err != nil {
			s.cloudOnline.Store(false)
			s.lastErr.Store(err.Error())
			ids := make([]string, 0, len(events))
			for _, ev := range events {
				ids = append(ids, ev.EventID)
			}
			_ = s.outbox.RecordSyncFailure(ctx, ids, err.Error())
			return total, err
		}
		if err := s.outbox.MarkCloudSynced(ctx, accepted, time.Now().UnixMilli()); err != nil {
			return total, err
		}
		s.cloudOnline.Store(true)
		s.lastErr.Store("")
		total += len(accepted)
		cursor = maxSeq
		if !hasMore {
			return total, nil
		}
	}
}

// Run loops in the background until ctx is cancelled. It stays alive even if IngestURL
// starts empty so configuring Telemetry in cklauncher activates shipping immediately.
func (s *Shipper) Run(ctx context.Context) {
	cfg := s.Config()
	backoff := cfg.FlushInterval
	if backoff <= 0 {
		backoff = 15 * time.Second
	}
	maxBackoff := 5 * time.Minute
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wakeCh:
			cfg = s.Config()
			backoff = cfg.FlushInterval
			if backoff <= 0 {
				backoff = 15 * time.Second
			}
			if cfg.IngestURL != "" {
				_, _ = s.FlushNow(ctx)
			}
			timer.Reset(backoff)
		case reply := <-s.triggerCh:
			_, err := s.FlushNow(ctx)
			reply <- err
			cfg = s.Config()
			backoff = cfg.FlushInterval
			timer.Reset(backoff)
		case <-timer.C:
			cfg = s.Config()
			if cfg.IngestURL == "" {
				timer.Reset(15 * time.Second)
				continue
			}
			if _, err := s.FlushNow(ctx); err != nil {
				backoff = min(backoff*2, maxBackoff)
			} else {
				backoff = cfg.FlushInterval
			}
			timer.Reset(backoff)
		}
	}
}

func (s *Shipper) uploadBatchWithConfig(ctx context.Context, cfg ShipperConfig, events []Event) ([]string, int, error) {
	nowMs := time.Now().UnixMilli()
	env := BatchEnvelope{
		InstallationID: cfg.InstallationID,
		AppName:        cfg.AppName,
		SenderDeviceID: cfg.DeviceID,
		SentAtMs:       nowMs,
		Events:         events,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, 0, ckframework.ErrInternal.Wrap(err, "marshal telemetry batch")
	}

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(raw); err != nil {
		return nil, 0, ckframework.ErrInternal.Wrap(err, "gzip telemetry batch")
	}
	if err := gz.Close(); err != nil {
		return nil, 0, ckframework.ErrInternal.Wrap(err, "close gzip batch")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.IngestURL, &gzBuf)
	if err != nil {
		return nil, 0, ckframework.ErrInternal.Wrap(err, "build ingest request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-CK-Device-ID", cfg.DeviceID)
	req.Header.Set("X-CK-Installation-ID", cfg.InstallationID)
	req.Header.Set("X-CK-Timestamp", strconv.FormatInt(nowMs, 10))
	if cfg.APIKey != "" {
		req.Header.Set("X-CK-API-Key", cfg.APIKey)
	}
	if len(cfg.PrivateKey) == ed25519.PrivateKeySize {
		pub := cfg.PrivateKey.Public().(ed25519.PublicKey)
		sig := ed25519.Sign(cfg.PrivateKey, raw)
		req.Header.Set("X-CK-Public-Key", base64.StdEncoding.EncodeToString(pub))
		req.Header.Set("X-CK-Signature", base64.StdEncoding.EncodeToString(sig))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, ckframework.ErrBridgeUnavailable.Wrap(err, "upload telemetry batch")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, ckframework.ErrBridgeUnavailable.New("telemetry ingest returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed IngestResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, resp.StatusCode, ckframework.ErrInternal.Wrap(err, "decode telemetry ingest response")
	}
	if len(parsed.AcceptedIDs) == 0 {
		ids := make([]string, 0, len(events))
		for _, ev := range events {
			ids = append(ids, ev.EventID)
		}
		return ids, resp.StatusCode, nil
	}
	return parsed.AcceptedIDs, resp.StatusCode, nil
}

// VerifyBatchSignature validates X-CK-Signature against a known Ed25519 public key.
func VerifyBatchSignature(pub ed25519.PublicKey, rawJSON []byte, sigB64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return ckframework.ErrUnauthorized.Wrap(err, "invalid signature encoding")
	}
	if !ed25519.Verify(pub, rawJSON, sig) {
		return ckframework.ErrUnauthorized.New("invalid batch signature")
	}
	return nil
}

// FormatKeyHint returns a concise fingerprint label for logs.
func FormatKeyHint(deviceID string) string {
	return fmt.Sprintf("key:%s", deviceID)
}
