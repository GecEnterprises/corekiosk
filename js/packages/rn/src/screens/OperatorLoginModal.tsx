import React, { useState } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";
import { useKiosk } from "../hooks.js";
import { OpColors, OpFonts, OpMetrics } from "../operatorTheme.js";

export function OperatorLoginModal() {
  const { loginOperator, closeOperator, state } = useKiosk();
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

  const appTitle = state.schema?.app_name || "Kiosk";

  return (
    <KeyboardAvoidingView style={styles.root} behavior={Platform.OS === "ios" ? "padding" : undefined}>
      <View style={styles.sheet}>
        <View style={styles.headerBlock}>
          <Text style={styles.title}>Operator Console</Text>
          <Text style={styles.subtitle}>Sign in to configure {appTitle}, manage settings, or view records.</Text>
        </View>

        <View style={styles.group}>
          <Text style={styles.label}>Username</Text>
          <TextInput
            value={username}
            onChangeText={setUsername}
            placeholder="operator"
            placeholderTextColor={OpColors.inkMuted}
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
            placeholder="Enter booth password"
            placeholderTextColor={OpColors.inkMuted}
            secureTextEntry
            autoFocus
            editable={!busy}
            onSubmitEditing={onLogin}
            style={styles.input}
          />
        </View>

        {error ? (
          <View style={styles.errorBox}>
            <Text style={styles.errorText}>{error}</Text>
          </View>
        ) : null}

        <View style={styles.actions}>
          <Pressable
            style={({ pressed }) => [styles.secondaryBtn, pressed && styles.btnPressed]}
            onPress={closeOperator}
            disabled={busy}
          >
            <Text style={styles.secondaryText}>Back to Kiosk</Text>
          </Pressable>
          <Pressable
            style={({ pressed }) => [styles.primaryBtn, pressed && { opacity: 0.88 }]}
            onPress={onLogin}
            disabled={busy}
          >
            {busy ? <ActivityIndicator color={OpColors.inkInverse} /> : <Text style={styles.primaryText}>Unlock</Text>}
          </Pressable>
        </View>
      </View>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: OpColors.ground,
    justifyContent: "center",
    alignItems: "center",
    padding: 24,
  },
  sheet: {
    width: "100%",
    maxWidth: 460,
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
  errorBox: {
    backgroundColor: OpColors.dangerBg,
    borderRadius: OpMetrics.radiusSm,
    paddingHorizontal: 14,
    paddingVertical: 12,
  },
  errorText: { color: OpColors.danger, fontSize: 15, fontWeight: "600", fontFamily: OpFonts.sans },
  actions: { flexDirection: "row", justifyContent: "space-between", gap: 12, marginTop: 6 },
  primaryBtn: {
    minHeight: 52,
    minWidth: 140,
    backgroundColor: OpColors.selectedBg,
    paddingHorizontal: 28,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  primaryText: { color: OpColors.selectedFg, fontWeight: "700", fontSize: 17, fontFamily: OpFonts.sans },
  secondaryBtn: {
    minHeight: 52,
    backgroundColor: OpColors.recessed,
    paddingHorizontal: 20,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  btnPressed: { backgroundColor: OpColors.hover },
  secondaryText: { color: OpColors.ink, fontWeight: "600", fontSize: 16, fontFamily: OpFonts.sans },
});
