//go:build windows

package cktelemetry

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

// CollectWifiInfo queries Windows WLAN status via netsh without flashing a console window.
func CollectWifiInfo(ctx context.Context) ckframework.WifiInfo {
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, "netsh", "wlan", "show", "interfaces")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return ckframework.WifiInfo{}
	}
	var snap ckframework.WifiInfo
	for _, rawLine := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(rawLine)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "state":
			snap.Connected = strings.EqualFold(val, "connected")
		case "ssid":
			if snap.SSID == "" {
				snap.SSID = val
			}
		case "bssid", "ap bssid":
			snap.BSSID = val
		case "radio type":
			snap.RadioType = val
		case "signal":
			val = strings.TrimSuffix(val, "%")
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				snap.SignalPct = n
			}
		}
	}
	return snap
}

// StartHostCollector periodically checks Windows Wi-Fi state (disconnects & BSSID roams),
// notifies onWifi so ckserver/cklauncher can display the active SSID live, and logs vitals.
func StartHostCollector(ctx context.Context, outbox *Outbox, interval time.Duration, onWifi func(ckframework.WifiInfo)) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		var prev ckframework.WifiInfo
		first := true
		vitalsEvery := 3
		tickCount := 0
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		sample := func() {
			cur := CollectWifiInfo(ctx)
			if onWifi != nil {
				onWifi(cur)
			}
			if !first {
				if prev.Connected && !cur.Connected {
					_, _ = outbox.Record(ctx, Event{
						Category: "wifi",
						Kind:     "net.wifi_disconnected",
						Severity: "warn",
						Payload: map[string]any{
							"prev_ssid":  prev.SSID,
							"prev_bssid": prev.BSSID,
						},
					})
				} else if !prev.Connected && cur.Connected {
					_, _ = outbox.Record(ctx, Event{
						Category: "wifi",
						Kind:     "net.wifi_connected",
						Severity: "info",
						Payload: map[string]any{
							"ssid":       cur.SSID,
							"bssid":      cur.BSSID,
							"signal_pct": cur.SignalPct,
						},
					})
				} else if cur.Connected && prev.BSSID != "" && cur.BSSID != "" && prev.BSSID != cur.BSSID {
					_, _ = outbox.Record(ctx, Event{
						Category: "wifi",
						Kind:     "net.wifi_roamed",
						Severity: "warn",
						Payload: map[string]any{
							"ssid":       cur.SSID,
							"from_bssid": prev.BSSID,
							"to_bssid":   cur.BSSID,
							"signal_pct": cur.SignalPct,
						},
					})
				}
			}
			first = false
			prev = cur

			if tickCount%vitalsEvery == 0 {
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				_, _ = outbox.Record(ctx, Event{
					Category: "system",
					Kind:     "system.vitals",
					Severity: "debug",
					Payload: map[string]any{
						"go_heap_mb":     ms.Alloc / (1024 * 1024),
						"goroutines":     runtime.NumGoroutine(),
						"wifi_connected": cur.Connected,
						"wifi_ssid":      cur.SSID,
						"wifi_bssid":     cur.BSSID,
						"wifi_signal":    cur.SignalPct,
					},
				})
			}
			tickCount++
		}

		sample()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
}
