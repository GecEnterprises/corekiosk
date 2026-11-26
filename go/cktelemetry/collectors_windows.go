//go:build windows

package cktelemetry

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"golang.org/x/sys/windows"
)

var (
	ole32DLL             = windows.NewLazySystemDLL("ole32.dll")
	oleaut32DLL          = windows.NewLazySystemDLL("oleaut32.dll")
	iphlpapiDLL          = windows.NewLazySystemDLL("iphlpapi.dll")
	procCoInitializeEx   = ole32DLL.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32DLL.NewProc("CoUninitialize")
	procCoCreateInst     = ole32DLL.NewProc("CoCreateInstance")
	procSysFreeString    = oleaut32DLL.NewProc("SysFreeString")
	procNotifyAddrChange = iphlpapiDLL.NewProc("NotifyAddrChange")

	// netshBlockedUntilMs avoids repeatedly spawning netsh.exe every few seconds when
	// Windows 11 24H2+ blocks WlanQueryInterface due to Location Services being disabled.
	netshBlockedUntilMs atomic.Int64
)

const (
	coinitMultithreaded = 0x0
	clsctxAll           = 0x17
	rpcEChangedMode     = 0x80010106
	ifTypeIEEE80211     = 71
)

var (
	clsidNetworkListManager = windows.GUID{
		Data1: 0xDCB00C01, Data2: 0x570F, Data3: 0x4A9B,
		Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B},
	}
	iidINetworkListManager = windows.GUID{
		Data1: 0xDCB00000, Data2: 0x570F, Data3: 0x4A9B,
		Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B},
	}
)

// CollectWifiInfo queries Windows WLAN status.
// First checks in-process via GetAdaptersAddresses whether any IEEE 802.11 adapter is Up
// (sub-millisecond disconnect/unplug detection). When an adapter is Up, tries `netsh` if
// permitted (for BSSID/signal %) and falls back to Network List Manager COM (works on
// Windows 11 24H2+ without Location Services or elevation).
func CollectWifiInfo(ctx context.Context) ckframework.WifiInfo {
	wifiGUIDs := activeWifiAdapterGUIDs()
	if len(wifiGUIDs) == 0 {
		return ckframework.WifiInfo{}
	}

	nowMs := time.Now().UnixMilli()
	if nowMs >= netshBlockedUntilMs.Load() {
		if snap := collectWifiViaNetsh(ctx); snap.Connected && snap.SSID != "" {
			netshBlockedUntilMs.Store(0)
			return snap
		}
		if nlmSnap := collectWifiViaNLMWithGUIDs(wifiGUIDs); nlmSnap.Connected && nlmSnap.SSID != "" {
			// netsh failed (e.g. Location permission disabled) while NLM succeeded; back off netsh for 60s.
			netshBlockedUntilMs.Store(nowMs + 60_000)
			return nlmSnap
		}
		return ckframework.WifiInfo{}
	}

	return collectWifiViaNLMWithGUIDs(wifiGUIDs)
}

func collectWifiViaNetsh(ctx context.Context) ckframework.WifiInfo {
	tctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
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

type comObject struct {
	vtbl *comVtbl
}

type comVtbl struct {
	slots [20]uintptr
}

func comRelease(obj *comObject) {
	if obj == nil || obj.vtbl == nil {
		return
	}
	_, _, _ = syscall.SyscallN(obj.vtbl.slots[2], uintptr(unsafe.Pointer(obj)))
}

func formatGUID(g windows.GUID) string {
	return strings.ToUpper(fmt.Sprintf("{%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		g.Data1, g.Data2, g.Data3,
		g.Data4[0], g.Data4[1],
		g.Data4[2], g.Data4[3], g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7]))
}

func activeWifiAdapterGUIDs() map[string]struct{} {
	res := map[string]struct{}{}
	size := uint32(15 * 1024)
	var buf []byte
	for i := 0; i < 3; i++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(
			windows.AF_UNSPEC,
			windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])),
			&size,
		)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return res
		}
	}
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.IfType != ifTypeIEEE80211 || aa.OperStatus != windows.IfOperStatusUp {
			continue
		}
		if aa.AdapterName == nil {
			continue
		}
		guid := strings.ToUpper(strings.TrimSpace(windows.BytePtrToString(aa.AdapterName)))
		if guid != "" {
			res[guid] = struct{}{}
		}
	}
	return res
}

func bstrToString(bstr uintptr) string {
	if bstr == 0 {
		return ""
	}
	byteLen := *(*uint32)(unsafe.Pointer(bstr - 4))
	u16Len := byteLen / 2
	if u16Len == 0 {
		return ""
	}
	s := unsafe.Slice((*uint16)(unsafe.Pointer(bstr)), u16Len)
	return windows.UTF16ToString(s)
}

func collectWifiViaNLMWithGUIDs(wifiGUIDs map[string]struct{}) ckframework.WifiInfo {
	if len(wifiGUIDs) == 0 {
		return ckframework.WifiInfo{}
	}

	ch := make(chan ckframework.WifiInfo, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
		if hr == 0 || hr == 1 {
			defer procCoUninitialize.Call()
		} else if uint32(hr) != rpcEChangedMode {
			ch <- ckframework.WifiInfo{}
			return
		}

		var nlm *comObject
		hr, _, _ = procCoCreateInst.Call(
			uintptr(unsafe.Pointer(&clsidNetworkListManager)),
			0,
			clsctxAll,
			uintptr(unsafe.Pointer(&iidINetworkListManager)),
			uintptr(unsafe.Pointer(&nlm)),
		)
		if int32(hr) < 0 || nlm == nil {
			ch <- ckframework.WifiInfo{}
			return
		}
		defer comRelease(nlm)

		var enumConn *comObject
		hr, _, _ = syscall.SyscallN(nlm.vtbl.slots[9], uintptr(unsafe.Pointer(nlm)), uintptr(unsafe.Pointer(&enumConn)))
		if int32(hr) < 0 || enumConn == nil {
			ch <- ckframework.WifiInfo{}
			return
		}
		defer comRelease(enumConn)

		for {
			var conn *comObject
			var fetched uint32
			hr, _, _ = syscall.SyscallN(
				enumConn.vtbl.slots[8],
				uintptr(unsafe.Pointer(enumConn)),
				1,
				uintptr(unsafe.Pointer(&conn)),
				uintptr(unsafe.Pointer(&fetched)),
			)
			if int32(hr) < 0 || fetched == 0 || conn == nil {
				break
			}

			var isConn int16
			hrConn, _, _ := syscall.SyscallN(conn.vtbl.slots[9], uintptr(unsafe.Pointer(conn)), uintptr(unsafe.Pointer(&isConn)))

			var adapterGUID windows.GUID
			hrGUID, _, _ := syscall.SyscallN(conn.vtbl.slots[12], uintptr(unsafe.Pointer(conn)), uintptr(unsafe.Pointer(&adapterGUID)))

			if int32(hrConn) >= 0 && isConn != 0 && int32(hrGUID) >= 0 {
				if _, ok := wifiGUIDs[formatGUID(adapterGUID)]; ok {
					var netObj *comObject
					hrNet, _, _ := syscall.SyscallN(conn.vtbl.slots[7], uintptr(unsafe.Pointer(conn)), uintptr(unsafe.Pointer(&netObj)))
					if int32(hrNet) >= 0 && netObj != nil {
						var bstr uintptr
						hrName, _, _ := syscall.SyscallN(netObj.vtbl.slots[7], uintptr(unsafe.Pointer(netObj)), uintptr(unsafe.Pointer(&bstr)))
						name := ""
						if int32(hrName) >= 0 && bstr != 0 {
							name = strings.TrimSpace(bstrToString(bstr))
							_, _, _ = procSysFreeString.Call(bstr)
						}
						comRelease(netObj)
						if name != "" {
							comRelease(conn)
							ch <- ckframework.WifiInfo{
								Connected: true,
								SSID:      name,
							}
							return
						}
					}
				}
			}
			comRelease(conn)
		}

		ch <- ckframework.WifiInfo{}
	}()

	return <-ch
}

// WatchNetworkChanges subscribes to Windows kernel IPv4 address & adapter state notifications
// via iphlpapi!NotifyAddrChange and emits coalesced signals whenever Wi-Fi or any network
// adapter connects, disconnects, or changes IP addresses.
func WatchNetworkChanges(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	stopEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		close(out)
		return out
	}
	addrEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(stopEv)
		close(out)
		return out
	}

	go func() {
		<-ctx.Done()
		_ = windows.SetEvent(stopEv)
	}()

	go func() {
		defer close(out)
		defer windows.CloseHandle(stopEv)
		defer windows.CloseHandle(addrEv)

		handles := []windows.Handle{stopEv, addrEv}
		for {
			_ = windows.ResetEvent(addrEv)
			var ov windows.Overlapped
			ov.HEvent = addrEv
			var handle uintptr

			ret, _, _ := procNotifyAddrChange.Call(
				uintptr(unsafe.Pointer(&handle)),
				uintptr(unsafe.Pointer(&ov)),
			)
			if ret != 0 && windows.Errno(ret) != windows.ERROR_IO_PENDING {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
					continue
				}
			}

			waitRes, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
			if err != nil || waitRes == windows.WAIT_OBJECT_0 {
				return
			}

			// Emit immediately so disconnects reflect in <50ms, then emit one follow-up after
			// 350ms so DHCP address assignment settling after link-up is also captured.
			select {
			case out <- struct{}{}:
			default:
			}
			time.AfterFunc(350*time.Millisecond, func() {
				if ctx.Err() == nil {
					select {
					case out <- struct{}{}:
					default:
					}
				}
			})
		}
	}()

	return out
}

// StartHostCollector reacts immediately to OS adapter/IP events (via WatchNetworkChanges)
// as well as periodic polling to track Wi-Fi disconnects, reconnects, BSSID roams, and vitals.
func StartHostCollector(ctx context.Context, outbox *Outbox, interval time.Duration, onWifi func(ckframework.WifiInfo)) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	netChanges := WatchNetworkChanges(ctx)

	go func() {
		var prev ckframework.WifiInfo
		first := true
		lastVitals := time.Time{}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		sample := func(forceNetshRetry bool) {
			if forceNetshRetry {
				// On network transitions, allow netsh a fresh attempt in case Location was enabled.
				netshBlockedUntilMs.Store(0)
			}
			cur := CollectWifiInfo(ctx)
			if onWifi != nil {
				onWifi(cur)
			}
			if !first && outbox != nil {
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
				} else if cur.Connected && prev.SSID != "" && cur.SSID != "" && prev.SSID != cur.SSID {
					_, _ = outbox.Record(ctx, Event{
						Category: "wifi",
						Kind:     "net.wifi_switched",
						Severity: "info",
						Payload: map[string]any{
							"from_ssid":  prev.SSID,
							"to_ssid":    cur.SSID,
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

			if outbox != nil && time.Since(lastVitals) >= 30*time.Second {
				lastVitals = time.Now()
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
		}

		sample(false)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-netChanges:
				if !ok {
					netChanges = nil
					continue
				}
				sample(true)
			case <-ticker.C:
				sample(false)
			}
		}
	}()
}
