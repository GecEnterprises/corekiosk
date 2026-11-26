//go:build windows

package cktelemetry

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// WifiSnapshot describes the current Windows Wi-Fi interface state.
type WifiSnapshot struct {
	Connected     bool   `json:"connected"`
	SSID          string `json:"ssid,omitempty"`
	BSSID         string `json:"bssid,omitempty"`
	SignalPct     int    `json:"signal_pct,omitempty"`
	RadioType     string `json:"radio_type,omitempty"`
	ReceiveMbps   string `json:"receive_mbps,omitempty"`
	TransmitMbps  string `json:"transmit_mbps,omitempty"`
}

// CollectWifiSnapshot queries Windows WLAN status via netsh without external CGO dependencies.
func CollectWifiSnapshot(ctx context.Context) WifiSnapshot {
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(tctx, "netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		return WifiSnapshot{}
	}
	var snap WifiSnapshot
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
		case "receive rate (mbps)":
			snap.ReceiveMbps = val
		case "transmit rate (mbps)":
			snap.TransmitMbps = val
		case "signal":
			val = strings.TrimSuffix(val, "%")
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				snap.SignalPct = n
			}
		}
	}
	return snap
}

// StartHostCollector periodically records PC Wi-Fi state transitions (disconnects & BSSID roams)
// and runtime vitals into the local outbox.
func StartHostCollector(ctx context.Context, outbox *Outbox, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		var prev WifiSnapshot
		first := true
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		sample := func() {
			cur := CollectWifiSnapshot(ctx)
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

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			_, _ = outbox.Record(ctx, Event{
				Category: "system",
				Kind:     "system.vitals",
				Severity: "debug",
				Payload: map[string]any{
					"go_heap_mb":    ms.Alloc / (1024 * 1024),
					"goroutines":    runtime.NumGoroutine(),
					"wifi_connected": cur.Connected,
					"wifi_ssid":     cur.SSID,
					"wifi_bssid":    cur.BSSID,
					"wifi_signal":   cur.SignalPct,
				},
			})
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
