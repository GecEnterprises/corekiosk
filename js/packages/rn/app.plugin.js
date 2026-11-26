// Expo config plugin: enables LAN cleartext HTTP/WS on Android for local booth servers.
module.exports = function withCoreKiosk(config) {
  if (!config.android) config.android = {};
  if (!config.android.permissions) config.android.permissions = [];
  const needed = ["android.permission.ACCESS_NETWORK_STATE", "android.permission.ACCESS_WIFI_STATE"];
  for (const perm of needed) {
    if (!config.android.permissions.includes(perm)) {
      config.android.permissions.push(perm);
    }
  }
  return config;
};
