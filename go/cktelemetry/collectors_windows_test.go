//go:build windows

package cktelemetry

import (
	"context"
	"testing"
	"time"
)

func TestCollectWifiInfoAndWatchNetworkChanges(t *testing.T) {
	info := CollectWifiInfo(context.Background())
	t.Logf("wifi: connected=%v ssid=%q bssid=%q signal=%d radio=%q",
		info.Connected, info.SSID, info.BSSID, info.SignalPct, info.RadioType)

	ctx, cancel := context.WithCancel(context.Background())
	ch := WatchNetworkChanges(ctx)
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			// Drain any in-flight event and verify close follows.
			select {
			case <-ch:
			case <-time.After(1 * time.Second):
				t.Fatal("expected WatchNetworkChanges channel to close on ctx cancel")
			}
		}
	case <-time.After(1 * time.Second):
		t.Fatal("expected WatchNetworkChanges channel to close promptly on ctx cancel")
	}
}
