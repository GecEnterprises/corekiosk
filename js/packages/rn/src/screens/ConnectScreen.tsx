import React, { useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { useKiosk } from "../hooks.js";

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
    <View style={styles.root}>
      <View style={styles.card}>
        <Text style={styles.title}>Connect to Kiosk</Text>

        <View style={styles.group}>
          <Text style={styles.label}>Server Address</Text>
          <TextInput
            value={address}
            onChangeText={setAddress}
            placeholder="192.168.1.10:8080"
            placeholderTextColor="#64748B"
            autoCapitalize="none"
            autoCorrect={false}
            editable={!busy}
            style={styles.input}
          />
        </View>

        <View style={styles.group}>
          <Text style={styles.label}>Pair Code (first time only)</Text>
          <TextInput
            value={pairCode}
            onChangeText={setPairCode}
            placeholder="6-digit code"
            placeholderTextColor="#64748B"
            keyboardType="number-pad"
            maxLength={6}
            editable={!busy}
            style={styles.input}
          />
        </View>

        {error ? <Text style={styles.error}>{error}</Text> : null}

        <View style={styles.actions}>
          {paired ? (
            <Pressable style={styles.secondaryBtn} onPress={closeConnect} disabled={busy}>
              <Text style={styles.secondaryText}>Cancel</Text>
            </Pressable>
          ) : null}
          <Pressable style={styles.primaryBtn} onPress={onSubmit} disabled={busy}>
            {busy ? <ActivityIndicator color="#0F172A" /> : <Text style={styles.primaryText}>Connect</Text>}
          </Pressable>
        </View>

        {clientDeviceId ? <Text style={styles.keyHint}>Key {clientDeviceId}</Text> : null}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: "#0F131A",
    justifyContent: "center",
    alignItems: "center",
    padding: 24,
  },
  card: {
    width: "100%",
    maxWidth: 440,
    backgroundColor: "#161B24",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 14,
    padding: 24,
    gap: 16,
  },
  title: { fontSize: 22, fontWeight: "700", color: "#F1F5F9" },
  group: { gap: 6 },
  label: { fontSize: 13, fontWeight: "600", color: "#CBD5E1" },
  input: {
    backgroundColor: "#10141C",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 12,
    color: "#F8FAFC",
    fontSize: 16,
  },
  error: { color: "#F87171", fontSize: 13, fontWeight: "600" },
  actions: { flexDirection: "row", justifyContent: "flex-end", gap: 10, marginTop: 4 },
  primaryBtn: {
    backgroundColor: "#38BDF8",
    paddingHorizontal: 22,
    paddingVertical: 12,
    borderRadius: 10,
    minWidth: 110,
    alignItems: "center",
  },
  primaryText: { color: "#0B1120", fontWeight: "700", fontSize: 15 },
  secondaryBtn: {
    backgroundColor: "#1E2532",
    borderColor: "#334155",
    borderWidth: 1,
    paddingHorizontal: 18,
    paddingVertical: 12,
    borderRadius: 10,
  },
  secondaryText: { color: "#E2E8F0", fontWeight: "600", fontSize: 15 },
  keyHint: { color: "#64748B", fontSize: 12, fontFamily: "monospace", textAlign: "right" },
});
