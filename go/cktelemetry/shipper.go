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
	"strconv"
	"sync/atomic"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

// ShipperConfig configures the background HTTPS/Firebase telemetry uploader.
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

// Shipper flushes unsynced rows from a local Outbox to Firebase Cloud Functions
// or a self-hosted cktelemetry-server with Ed25519 signatures and backoff.
type Shipper struct {
	outbox      *Outbox
	cfg         ShipperConfig
	client      *http.Client
	cloudOnline atomic.Bool
	lastErr     atomic.Value
	triggerCh   chan chan error
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
	}
	s.lastErr.Store("")
	return s
}

func (s *Shipper) CloudOnline() bool { return s.cloudOnline.Load() }

func (s *Shipper) LastError() string {
	v, _ := s.lastErr.Load().(string)
	return v
}

// FlushNow triggers an immediate sync attempt (used by cklauncher "Force Sync Now").
func (s *Shipper) FlushNow(ctx context.Context) (int, error) {
	if s.cfg.IngestURL == "" {
		s.cloudOnline.Store(false)
		return 0, nil
	}
	total := 0
	var cursor int64
	for {
		events, maxSeq, hasMore, err := s.outbox.PullUnsynced(ctx, cursor, s.cfg.BatchSize)
		if err != nil {
			return total, err
		}
		if len(events) == 0 {
			s.cloudOnline.Store(true)
			s.lastErr.Store("")
			return total, nil
		}
		accepted, err := s.uploadBatch(ctx, events)
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

// Run loops in the background until ctx is cancelled.
func (s *Shipper) Run(ctx context.Context) {
	if s.cfg.IngestURL == "" {
		return
	}
	backoff := s.cfg.FlushInterval
	maxBackoff := 5 * time.Minute
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case reply := <-s.triggerCh:
			_, err := s.FlushNow(ctx)
			reply <- err
			backoff = s.cfg.FlushInterval
			timer.Reset(backoff)
		case <-timer.C:
			if _, err := s.FlushNow(ctx); err != nil {
				backoff = min(backoff*2, maxBackoff)
			} else {
				backoff = s.cfg.FlushInterval
			}
			timer.Reset(backoff)
		}
	}
}

func (s *Shipper) uploadBatch(ctx context.Context, events []Event) ([]string, error) {
	nowMs := time.Now().UnixMilli()
	env := BatchEnvelope{
		InstallationID: s.cfg.InstallationID,
		AppName:        s.cfg.AppName,
		SenderDeviceID: s.cfg.DeviceID,
		SentAtMs:       nowMs,
		Events:         events,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, ckframework.ErrInternal.Wrap(err, "marshal telemetry batch")
	}

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(raw); err != nil {
		return nil, ckframework.ErrInternal.Wrap(err, "gzip telemetry batch")
	}
	if err := gz.Close(); err != nil {
		return nil, ckframework.ErrInternal.Wrap(err, "close gzip batch")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.IngestURL, &gzBuf)
	if err != nil {
		return nil, ckframework.ErrInternal.Wrap(err, "build ingest request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-CK-Device-ID", s.cfg.DeviceID)
	req.Header.Set("X-CK-Installation-ID", s.cfg.InstallationID)
	req.Header.Set("X-CK-Timestamp", strconv.FormatInt(nowMs, 10))
	if s.cfg.APIKey != "" {
		req.Header.Set("X-CK-API-Key", s.cfg.APIKey)
	}
	if len(s.cfg.PrivateKey) == ed25519.PrivateKeySize {
		sig := ed25519.Sign(s.cfg.PrivateKey, raw)
		req.Header.Set("X-CK-Signature", base64.StdEncoding.EncodeToString(sig))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, ckframework.ErrBridgeUnavailable.Wrap(err, "upload telemetry batch")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ckframework.ErrBridgeUnavailable.New("telemetry ingest returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	var parsed IngestResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, ckframework.ErrInternal.Wrap(err, "decode telemetry ingest response")
	}
	if len(parsed.AcceptedIDs) == 0 {
		// Accept all sent IDs if server returned 200 without explicit list.
		ids := make([]string, 0, len(events))
		for _, ev := range events {
			ids = append(ids, ev.EventID)
		}
		return ids, nil
	}
	return parsed.AcceptedIDs, nil
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
