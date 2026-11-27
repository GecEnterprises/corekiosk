// Expo config plugin: enables LAN cleartext HTTP/WS & Wi-Fi permissions on Android for local booth servers.
module.exports = function withCoreKiosk(config) {
  if (!config.android) config.android = {};
  if (!config.android.permissions) config.android.permissions = [];
  const needed = ["android.permission.ACCESS_NETWORK_STATE", "android.permission.ACCESS_WIFI_STATE"];
  for (const perm of needed) {
    if (!config.android.permissions.includes(perm)) {
      config.android.permissions.push(perm);
    }
  }

  try {
    const { withAndroidManifest } = require("expo/config-plugins");
    return withAndroidManifest(config, (mod) => {
      const app = mod.modResults?.manifest?.application?.[0];
      if (app && app.$) {
        app.$["android:usesCleartextTraffic"] = "true";
      }
      return mod;
    });
  } catch {
    return config;
  }
};
