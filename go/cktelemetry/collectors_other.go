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

// WatchNetworkChanges emits periodic wakeups on non-Windows platforms.
func WatchNetworkChanges(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}

func StartHostCollector(ctx context.Context, outbox *Outbox, interval time.Duration, onWifi func(ckframework.WifiInfo)) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastVitals := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur := CollectWifiInfo(ctx)
				if onWifi != nil {
					onWifi(cur)
				}
				if outbox != nil && time.Since(lastVitals) >= 30*time.Second {
					lastVitals = time.Now()
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
		}
	}()
}
