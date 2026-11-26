import React, { useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { useKiosk } from "../hooks.js";

export function OperatorLoginModal() {
  const { loginOperator, closeOperator } = useKiosk();
  const [username, setUsername] = useState("operator");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onLogin = async () => {
    setError(null);
    setBusy(true);
    try {
      await loginOperator(username.trim(), password);
      setPassword("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <View style={styles.root}>
      <View style={styles.card}>
        <Text style={styles.title}>Operator Login</Text>

        <View style={styles.group}>
          <Text style={styles.label}>Username</Text>
          <TextInput
            value={username}
            onChangeText={setUsername}
            placeholder="operator"
            placeholderTextColor="#64748B"
            autoCapitalize="none"
            autoCorrect={false}
            editable={!busy}
            style={styles.input}
          />
        </View>

        <View style={styles.group}>
          <Text style={styles.label}>Password</Text>
          <TextInput
            value={password}
            onChangeText={setPassword}
            placeholder="Password"
            placeholderTextColor="#64748B"
            secureTextEntry
            editable={!busy}
            onSubmitEditing={onLogin}
            style={styles.input}
          />
        </View>

        {error ? <Text style={styles.error}>{error}</Text> : null}

        <View style={styles.actions}>
          <Pressable style={styles.secondaryBtn} onPress={closeOperator} disabled={busy}>
            <Text style={styles.secondaryText}>Cancel</Text>
          </Pressable>
          <Pressable style={styles.primaryBtn} onPress={onLogin} disabled={busy}>
            {busy ? <ActivityIndicator color="#0B1120" /> : <Text style={styles.primaryText}>Unlock</Text>}
          </Pressable>
        </View>
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
    maxWidth: 400,
    backgroundColor: "#161B24",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 14,
    padding: 24,
    gap: 16,
  },
  title: { fontSize: 20, fontWeight: "700", color: "#F1F5F9" },
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
    minWidth: 100,
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
});
