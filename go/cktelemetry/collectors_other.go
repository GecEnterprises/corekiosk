//go:build !windows

package cktelemetry

import (
	"context"
	"runtime"
	"time"
)

type WifiSnapshot struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	BSSID     string `json:"bssid,omitempty"`
	SignalPct int    `json:"signal_pct,omitempty"`
}

func CollectWifiSnapshot(_ context.Context) WifiSnapshot {
	return WifiSnapshot{}
}

func StartHostCollector(ctx context.Context, outbox *Outbox, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				_, _ = outbox.Record(ctx, Event{
					Category: "system",
					Kind:     "system.vitals",
					Severity: "debug",
					Payload: map[string]any{
						"go_heap_mb": ms.Alloc / (1024 * 1024),
						"goroutines": runtime.NumGoroutine(),
					},
				})
			}
		}
	}()
}
