//go:build !windows

package cktelemetry

import (
	"context"
	"runtime"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

func CollectWifiInfo(_ context.Context) ckframework.WifiInfo {
	return ckframework.WifiInfo{}
}

func StartHostCollector(ctx context.Context, outbox *Outbox, interval time.Duration, onWifi func(ckframework.WifiInfo)) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur := CollectWifiInfo(ctx)
				if onWifi != nil {
					onWifi(cur)
				}
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
