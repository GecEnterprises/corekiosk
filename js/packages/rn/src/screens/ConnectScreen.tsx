import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";
import { discoveryAddress, rankDiscoveredServers, type DiscoveredServer } from "@corekiosk/client";
import { useKiosk } from "../hooks.js";
import { OpColors, OpFonts, OpMetrics } from "../operatorTheme.js";

function defaultServerAddress(): string {
  try {
    const loc = (globalThis as unknown as { location?: { hostname?: string } }).location;
    if (loc?.hostname) {
      return `${loc.hostname}:8080`;
    }
  } catch {}
  return "192.168.1.10:8080";
}

function sourceLabel(server: DiscoveredServer): string {
  const sources = server.sources ?? [];
  if (sources.includes("mdns")) return "mDNS";
  if (sources.includes("udp")) return "UDP";
  if (sources.includes("ssdp")) return "SSDP";
  return "LAN";
}

export function ConnectScreen() {
  const {
    serverUrl,
    paired,
    pinnedServer,
    pinnedServerIds,
    clientDeviceId,
    discovered,
    discovering,
    discoveryError,
    scanForServers,
    connectToDiscovered,
    connectOrPair,
    closeConnect,
  } = useKiosk();
  const [address, setAddress] = useState(serverUrl.replace(/^https?:\/\//, "") || defaultServerAddress());
  const [pairCode, setPairCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const codeInputRef = useRef<TextInput>(null);

  const nearby = useMemo(
    () => rankDiscoveredServers(discovered, pinnedServer?.serverId),
    [discovered, pinnedServer?.serverId]
  );

  useEffect(() => {
    void scanForServers(3500);
    // Run once when the pairing screen opens; manual retry is available below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onSubmit = async () => {
    setError(null);
    setBusy(true);
    try {
      await connectOrPair(address, pairCode || undefined);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const onPick = async (server: DiscoveredServer) => {
    setError(null);
    setAddress(discoveryAddress(server));
    if (pinnedServerIds.includes(server.serverId)) {
      setBusy(true);
      try {
        await connectToDiscovered(server);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setBusy(false);
      }
    } else {
      codeInputRef.current?.focus();
    }
  };

  return (
    <KeyboardAvoidingView style={styles.root} behavior={Platform.OS === "ios" ? "padding" : undefined}>
      <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
        <View style={styles.sheet}>
          <View style={styles.headerBlock}>
            <Text style={styles.title}>Pair Companion</Text>
            <Text style={styles.subtitle}>
              Pick a kiosk on this network, or enter the address shown on the kiosk display. Already-paired devices reconnect without a code.
            </Text>
          </View>

          <View style={styles.group}>
            <View style={styles.nearbyHeader}>
              <Text style={styles.label}>Nearby kiosks</Text>
              <Pressable
                onPress={() => void scanForServers(3500)}
                disabled={discovering}
                style={({ pressed }) => [styles.scanBtn, pressed && styles.btnPressed]}
              >
                {discovering ? (
                  <ActivityIndicator size="small" color={OpColors.ink} />
                ) : (
                  <Text style={styles.scanText}>Search again</Text>
                )}
              </Pressable>
            </View>

            {nearby.length === 0 ? (
              <View style={styles.emptyBox}>
                <Text style={styles.emptyText}>
                  {discovering
                    ? "Searching the LAN…"
                    : "No kiosks found. Make sure the tablet and kiosk are on the same Wi-Fi. You can still enter the address below."}
                </Text>
              </View>
            ) : (
              <View style={styles.nearbyList}>
                {nearby.map((server) => {
                  const isPaired = pinnedServerIds.includes(server.serverId);
                  return (
                    <Pressable
                      key={server.serverId}
                      onPress={() => void onPick(server)}
                      disabled={busy}
                      style={({ pressed }) => [styles.serverRow, pressed && styles.serverRowPressed]}
                    >
                      <View style={styles.serverMain}>
                        <Text style={styles.serverName} numberOfLines={1}>
                          {server.name || server.appName || "CoreKiosk"}
                        </Text>
                        <Text style={styles.serverAddr} numberOfLines={1}>
                          {discoveryAddress(server)}
                        </Text>
                      </View>
                      <View style={styles.serverMeta}>
                        <View style={styles.sourceBadge}>
                          <Text style={styles.sourceText}>{sourceLabel(server)}</Text>
                        </View>
                        {isPaired ? (
                          <View style={styles.pairedBadge}>
                            <Text style={styles.pairedText}>Paired</Text>
                          </View>
                        ) : null}
                      </View>
                    </Pressable>
                  );
                })}
              </View>
            )}
            {discoveryError ? <Text style={styles.discoveryError}>{discoveryError}</Text> : null}
          </View>

          <View style={styles.divider} />

          <View style={styles.group}>
            <Text style={styles.label}>Kiosk Address</Text>
            <TextInput
              value={address}
              onChangeText={setAddress}
              placeholder="192.168.1.10:8080"
              placeholderTextColor={OpColors.inkMuted}
              autoCapitalize="none"
              autoCorrect={false}
              editable={!busy}
              onSubmitEditing={onSubmit}
              style={[styles.input, styles.monoInput]}
            />
          </View>

          <View style={styles.group}>
            <Text style={styles.label}>6-Digit Pairing Code</Text>
            <TextInput
              ref={codeInputRef}
              value={pairCode}
              onChangeText={setPairCode}
              placeholder="Required on first connect"
              placeholderTextColor={OpColors.inkMuted}
              keyboardType="number-pad"
              maxLength={6}
              editable={!busy}
              onSubmitEditing={onSubmit}
              style={[styles.input, styles.codeInput]}
            />
          </View>

          {error ? (
            <View style={styles.errorBox}>
              <Text style={styles.errorText}>{error}</Text>
            </View>
          ) : null}

          <View style={styles.actions}>
            {paired ? (
              <Pressable
                style={({ pressed }) => [styles.secondaryBtn, pressed && styles.btnPressed]}
                onPress={closeConnect}
                disabled={busy}
              >
                <Text style={styles.secondaryText}>Cancel</Text>
              </Pressable>
            ) : null}
            <Pressable
              style={({ pressed }) => [styles.primaryBtn, pressed && { opacity: 0.88 }]}
              onPress={onSubmit}
              disabled={busy}
            >
              {busy ? <ActivityIndicator color={OpColors.inkInverse} /> : <Text style={styles.primaryText}>Connect</Text>}
            </Pressable>
          </View>

          {clientDeviceId ? (
            <View style={styles.footerMeta}>
              <Text style={styles.metaText}>Device ID: {clientDeviceId}</Text>
            </View>
          ) : null}
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: OpColors.ground },
  scroll: { flexGrow: 1, justifyContent: "center", alignItems: "center", padding: 24 },
  sheet: {
    width: "100%",
    maxWidth: 520,
    backgroundColor: OpColors.surface,
    borderColor: OpColors.line,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusLg,
    padding: 32,
    gap: 20,
  },
  headerBlock: { gap: 6 },
  title: {
    fontSize: 26,
    fontWeight: "700",
    color: OpColors.ink,
    fontFamily: OpFonts.sans,
    letterSpacing: -0.4,
  },
  subtitle: {
    fontSize: 15,
    lineHeight: 22,
    color: OpColors.inkSecondary,
    fontFamily: OpFonts.sans,
  },
  group: { gap: 8 },
  nearbyHeader: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 12 },
  label: {
    fontSize: 15,
    fontWeight: "600",
    color: OpColors.ink,
    fontFamily: OpFonts.sans,
  },
  scanBtn: {
    minHeight: 36,
    paddingHorizontal: 12,
    borderRadius: OpMetrics.radiusSm,
    backgroundColor: OpColors.recessed,
    alignItems: "center",
    justifyContent: "center",
  },
  scanText: { color: OpColors.ink, fontSize: 14, fontWeight: "600", fontFamily: OpFonts.sans },
  emptyBox: {
    backgroundColor: OpColors.ground,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 14,
    paddingVertical: 16,
  },
  emptyText: { color: OpColors.inkSecondary, fontSize: 14, lineHeight: 20, fontFamily: OpFonts.sans },
  nearbyList: { gap: 8 },
  serverRow: {
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    backgroundColor: OpColors.ground,
    borderColor: OpColors.line,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 14,
    paddingVertical: 12,
  },
  serverRowPressed: { backgroundColor: OpColors.hover },
  serverMain: { flex: 1, gap: 2 },
  serverName: { color: OpColors.ink, fontSize: 16, fontWeight: "700", fontFamily: OpFonts.sans },
  serverAddr: { color: OpColors.inkMuted, fontSize: 13, fontFamily: OpFonts.mono },
  serverMeta: { flexDirection: "row", alignItems: "center", gap: 6 },
  sourceBadge: {
    paddingHorizontal: 8,
    paddingVertical: 3,
    borderRadius: 999,
    backgroundColor: OpColors.recessed,
  },
  sourceText: { color: OpColors.inkSecondary, fontSize: 11, fontWeight: "700", fontFamily: OpFonts.sans },
  pairedBadge: {
    paddingHorizontal: 8,
    paddingVertical: 3,
    borderRadius: 999,
    backgroundColor: OpColors.accent,
  },
  pairedText: { color: OpColors.inkInverse, fontSize: 11, fontWeight: "700", fontFamily: OpFonts.sans },
  discoveryError: { color: OpColors.danger, fontSize: 13, fontFamily: OpFonts.sans },
  divider: { height: 1, backgroundColor: OpColors.line },
  input: {
    minHeight: 52,
    backgroundColor: OpColors.ground,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 16,
    paddingVertical: 12,
    color: OpColors.ink,
    fontSize: 18,
    fontFamily: OpFonts.sans,
  },
  monoInput: { fontFamily: OpFonts.mono },
  codeInput: { fontFamily: OpFonts.mono, fontSize: 22, fontWeight: "700", letterSpacing: 2 },
  errorBox: {
    backgroundColor: OpColors.dangerBg,
    borderRadius: OpMetrics.radiusSm,
    paddingHorizontal: 14,
    paddingVertical: 12,
  },
  errorText: { color: OpColors.danger, fontSize: 15, fontWeight: "600", fontFamily: OpFonts.sans },
  actions: { flexDirection: "row", justifyContent: "flex-end", gap: 12, marginTop: 4 },
  primaryBtn: {
    minHeight: 52,
    minWidth: 140,
    backgroundColor: OpColors.accent,
    paddingHorizontal: 28,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  primaryText: { color: OpColors.inkInverse, fontWeight: "700", fontSize: 17, fontFamily: OpFonts.sans },
  secondaryBtn: {
    minHeight: 52,
    backgroundColor: OpColors.recessed,
    paddingHorizontal: 22,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  btnPressed: { backgroundColor: OpColors.hover },
  secondaryText: { color: OpColors.ink, fontWeight: "600", fontSize: 17, fontFamily: OpFonts.sans },
  footerMeta: {
    borderTopWidth: 1,
    borderTopColor: OpColors.line,
    paddingTop: 14,
  },
  metaText: { color: OpColors.inkMuted, fontSize: 13, fontFamily: OpFonts.mono },
});
