import React, { useState } from "react";
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

export function ConnectScreen() {
  const { serverUrl, paired, clientDeviceId, connectOrPair, closeConnect } = useKiosk();
  const [address, setAddress] = useState(serverUrl.replace(/^https?:\/\//, "") || defaultServerAddress());
  const [pairCode, setPairCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

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

  return (
    <KeyboardAvoidingView style={styles.root} behavior={Platform.OS === "ios" ? "padding" : undefined}>
      <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
        <View style={styles.sheet}>
          <View style={styles.headerBlock}>
            <Text style={styles.title}>Pair Companion</Text>
            <Text style={styles.subtitle}>
              Enter the IP address shown on the kiosk display. Already-paired devices reconnect without a code.
            </Text>
          </View>

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
    maxWidth: 480,
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
  label: {
    fontSize: 15,
    fontWeight: "600",
    color: OpColors.ink,
    fontFamily: OpFonts.sans,
  },
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
