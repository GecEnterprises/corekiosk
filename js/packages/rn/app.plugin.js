import { createRequire } from "node:module";

// Expo config plugin: enables LAN cleartext HTTP/WS & Wi-Fi permissions on Android for local booth servers.
export default function withCoreKiosk(config) {
  if (!config.android) config.android = {};
  if (!config.android.permissions) config.android.permissions = [];
  const needed = [
    "android.permission.ACCESS_NETWORK_STATE",
    "android.permission.ACCESS_WIFI_STATE",
    "android.permission.CHANGE_WIFI_MULTICAST_STATE",
  ];
  for (const perm of needed) {
    if (!config.android.permissions.includes(perm)) {
      config.android.permissions.push(perm);
    }
  }

  try {
    const require = createRequire(import.meta.url);
    const { withAndroidManifest, withAppBuildGradle } = require("expo/config-plugins");
    config = withAndroidManifest(config, (mod) => {
      const app = mod.modResults?.manifest?.application?.[0];
      if (app && app.$) {
        app.$["android:usesCleartextTraffic"] = "true";
      }
      return mod;
    });
    config = withAppBuildGradle(config, (mod) => {
      if (
        typeof mod.modResults?.contents === "string" &&
        !mod.modResults.contents.includes("debuggableVariants = []")
      ) {
        mod.modResults.contents = mod.modResults.contents.replace(
          /\/\/\s*debuggableVariants\s*=\s*\[[^\]]*\]/,
          'debuggableVariants = []'
        );
      }
      return mod;
    });
    return config;
  } catch {
    return config;
  }
}
