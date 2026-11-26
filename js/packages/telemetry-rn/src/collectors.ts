import type { DeviceInfo, WifiInfo } from "@corekiosk/client";
import { CK_FRAMEWORK_VERSION, CK_PROTOCOL_VERSION } from "@corekiosk/client";
import type { LocalTelemetryOutbox } from "./outbox.js";

export interface NetInfoStateLike {
  type: string;
  isConnected: boolean | null;
  isInternetReachable: boolean | null;
  details?: unknown;
}

interface WifiDetails {
  ssid?: string | null;
  bssid?: string | null;
  strength?: number | null;
  frequency?: number | null;
  linkSpeed?: number | null;
  ipAddress?: string | null;
}

function asWifiDetails(details: unknown): WifiDetails | null {
  if (!details || typeof details !== "object") return null;
  return details as WifiDetails;
}

export interface NetInfoModuleLike {
  addEventListener(cb: (state: NetInfoStateLike) => void): () => void;
  fetch(): Promise<NetInfoStateLike>;
}

export interface BatteryModuleLike {
  getBatteryLevelAsync(): Promise<number>;
  getBatteryStateAsync(): Promise<number>;
  addBatteryLevelListener?(cb: (e: { batteryLevel: number }) => void): { remove(): void };
  addBatteryStateListener?(cb: (e: { batteryState: number }) => void): { remove(): void };
}

export interface CollectorHandles {
  stop(): void;
  getLatestWifi(): WifiInfo;
  isInternetReachable(): boolean;
}

/**
 * Starts Wi-Fi transition/roaming/sample & battery/power collectors into the local SQLite outbox.
 */
export function startDeviceCollectors(opts: {
  outbox: LocalTelemetryOutbox;
  netInfo?: NetInfoModuleLike;
  battery?: BatteryModuleLike;
  wifiSampleMs?: number;
  batterySampleMs?: number;
}): CollectorHandles {
  const cleanups: Array<() => void> = [];
  let latestWifi: WifiInfo = { connected: false };
  let prevWifi: { connected: boolean; ssid: string; bssid: string; ip: string; type: string } | null = null;
  let internetReachable = false;

  if (opts.netInfo) {
    const handleNet = (st: NetInfoStateLike) => {
      const details = asWifiDetails(st.details);
      const isWifi = st.type === "wifi" && Boolean(st.isConnected);
      const ssid = details?.ssid ?? "";
      const bssid = details?.bssid ?? "";
      const ip = details?.ipAddress ?? "";
      internetReachable = Boolean(st.isInternetReachable);

      latestWifi = {
        connected: isWifi,
        ssid: ssid || undefined,
        bssid: bssid || undefined,
        signal_pct: details?.strength ?? undefined,
      };

      if (prevWifi) {
        if (prevWifi.connected && !isWifi) {
          void opts.outbox.record({
            category: "wifi",
            kind: "net.wifi_disconnected",
            severity: "warn",
            payload: { prev_ssid: prevWifi.ssid, prev_bssid: prevWifi.bssid, next_type: st.type },
          });
        } else if (!prevWifi.connected && isWifi) {
          void opts.outbox.record({
            category: "wifi",
            kind: "net.wifi_connected",
            severity: "info",
            payload: { ssid, bssid, ip, strength: details?.strength, frequency: details?.frequency },
          });
        } else if (isWifi && prevWifi.bssid && bssid && prevWifi.bssid !== bssid) {
          void opts.outbox.record({
            category: "wifi",
            kind: "net.wifi_roamed",
            severity: "warn",
            payload: { ssid, from_bssid: prevWifi.bssid, to_bssid: bssid, strength: details?.strength },
          });
        }
        if (prevWifi.type !== st.type) {
          void opts.outbox.record({
            category: "wifi",
            kind: "net.transport_changed",
            severity: "info",
            payload: { from: prevWifi.type, to: st.type, internet_reachable: internetReachable },
          });
        }
        if (prevWifi.ip && ip && prevWifi.ip !== ip) {
          void opts.outbox.record({
            category: "wifi",
            kind: "net.ip_changed",
            severity: "info",
            payload: { old_ip: prevWifi.ip, new_ip: ip },
          });
        }
      }
      prevWifi = { connected: isWifi, ssid, bssid, ip, type: st.type };
    };

    const unsub = opts.netInfo.addEventListener(handleNet);
    cleanups.push(unsub);
    void opts.netInfo.fetch().then(handleNet).catch(() => undefined);

    const wifiTimer = setInterval(() => {
      void opts.netInfo?.fetch().then((st) => {
        const details = asWifiDetails(st.details);
        void opts.outbox.record({
          category: "wifi",
          kind: "net.wifi_sample",
          severity: "debug",
          payload: {
            type: st.type,
            connected: Boolean(st.isConnected),
            internet_reachable: Boolean(st.isInternetReachable),
            ssid: details?.ssid ?? "",
            bssid: details?.bssid ?? "",
            strength: details?.strength ?? null,
            frequency: details?.frequency ?? null,
            link_speed: details?.linkSpeed ?? null,
            ip: details?.ipAddress ?? "",
          },
        });
      });
    }, opts.wifiSampleMs ?? 30_000);
    cleanups.push(() => clearInterval(wifiTimer));
  }

  if (opts.battery) {
    // expo-battery BatteryState: 1=UNPLUGGED, 2=CHARGING, 3=FULL
    let prevCharging: boolean | null = null;
    let prevPct: number | null = null;

    const sampleBattery = async () => {
      try {
        const level = await opts.battery!.getBatteryLevelAsync();
        const state = await opts.battery!.getBatteryStateAsync();
        const pct = Math.round(level * 100);
        const charging = state === 2 || state === 3;

        if (prevCharging !== null && prevCharging && !charging) {
          await opts.outbox.record({
            category: "battery",
            kind: "battery.unplugged",
            severity: "warn",
            payload: { level_pct: pct },
          });
        } else if (prevCharging !== null && !prevCharging && charging) {
          await opts.outbox.record({
            category: "battery",
            kind: "battery.plugged_in",
            severity: "info",
            payload: { level_pct: pct },
          });
        }

        if (prevPct !== null && prevPct >= 20 && pct < 20 && !charging) {
          await opts.outbox.record({
            category: "battery",
            kind: "battery.low",
            severity: "warn",
            payload: { level_pct: pct, is_charging: charging },
          });
        } else if (prevPct !== null && prevPct >= 10 && pct < 10 && !charging) {
          await opts.outbox.record({
            category: "battery",
            kind: "battery.critical",
            severity: "critical",
            payload: { level_pct: pct, is_charging: charging },
          });
        }

        prevCharging = charging;
        prevPct = pct;

        await opts.outbox.record({
          category: "battery",
          kind: "battery.sample",
          severity: "debug",
          payload: { level_pct: pct, is_charging: charging, battery_state: state },
        });
      } catch {
        // Ignore unavailable battery API on simulators
      }
    };

    void sampleBattery();
    const batTimer = setInterval(() => void sampleBattery(), opts.batterySampleMs ?? 60_000);
    cleanups.push(() => clearInterval(batTimer));
  }

  return {
    stop() {
      for (const fn of cleanups) fn();
    },
    getLatestWifi() {
      return latestWifi;
    },
    isInternetReachable() {
      return internetReachable;
    },
  };
}

/**
 * Builds canonical DeviceInfo metadata sent to ckserver on pairing & reconnect.
 */
export function buildDeviceInfo(opts: {
  deviceName?: string | null;
  deviceType?: string;
  manufacturer?: string | null;
  model?: string | null;
  osName?: string | null;
  osVersion?: string | null;
  appVersion?: string | null;
  wifi?: WifiInfo;
}): DeviceInfo {
  return {
    device_name: opts.deviceName || opts.model || "Kiosk Tablet",
    device_type: opts.deviceType || "android_tablet",
    manufacturer: opts.manufacturer || undefined,
    model: opts.model || undefined,
    os_name: opts.osName || undefined,
    os_version: opts.osVersion || undefined,
    app_version: opts.appVersion || "0.1.0",
    framework_version: CK_FRAMEWORK_VERSION,
    protocol_version: CK_PROTOCOL_VERSION,
    wifi: opts.wifi,
  };
}
