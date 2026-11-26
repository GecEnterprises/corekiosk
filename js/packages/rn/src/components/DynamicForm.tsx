import React from "react";
import { Pressable, StyleSheet, Switch, Text, TextInput, View } from "react-native";
import type { FormField } from "@corekiosk/client";

export interface DynamicFormProps {
  fields: FormField[];
  values: Record<string, unknown>;
  errors?: Record<string, string>;
  onChange(key: string, value: unknown): void;
  disabled?: boolean;
  dark?: boolean;
}

export function DynamicForm({ fields, values, errors = {}, onChange, disabled, dark = true }: DynamicFormProps) {
  return (
    <View style={styles.container}>
      {fields.map((field) => {
        const err = errors[field.key];
        const val = values[field.key];
        return (
          <View key={field.key} style={styles.field}>
            <Text style={[styles.label, dark ? styles.labelDark : styles.labelLight]}>
              {field.label}
              {field.required ? " *" : ""}
            </Text>

            {field.type === "bool" ? (
              <Switch
                value={Boolean(val)}
                onValueChange={(v) => onChange(field.key, v)}
                disabled={disabled}
              />
            ) : field.type === "enum" && field.options?.length ? (
              <View style={styles.pills}>
                {field.options.map((opt) => {
                  const selected = val === opt.value;
                  return (
                    <Pressable
                      key={opt.value}
                      disabled={disabled}
                      onPress={() => onChange(field.key, opt.value)}
                      style={[styles.pill, selected && styles.pillSelected]}
                    >
                      <Text style={[styles.pillText, selected && styles.pillTextSelected]}>{opt.label}</Text>
                    </Pressable>
                  );
                })}
              </View>
            ) : (
              <TextInput
                value={val !== undefined && val !== null ? String(val) : ""}
                onChangeText={(txt) => onChange(field.key, field.type === "number" ? Number(txt) : txt)}
                placeholder={field.placeholder ?? field.label}
                placeholderTextColor={dark ? "#7B8AA4" : "#7A849A"}
                editable={!disabled}
                multiline={field.type === "textarea"}
                keyboardType={
                  field.type === "email"
                    ? "email-address"
                    : field.type === "number"
                      ? "numeric"
                      : field.type === "phone"
                        ? "phone-pad"
                        : "default"
                }
                autoCapitalize={field.type === "email" || field.type === "handle" ? "none" : "words"}
                maxLength={field.max_len || undefined}
                style={[
                  styles.input,
                  dark ? styles.inputDark : styles.inputLight,
                  field.type === "textarea" && styles.textarea,
                  err ? styles.inputError : null,
                ]}
              />
            )}

            {err ? <Text style={styles.errorText}>{err}</Text> : null}
          </View>
        );
      })}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { gap: 12 },
  field: { gap: 5 },
  label: { fontSize: 13, fontWeight: "700" },
  labelDark: { color: "#D6DFEE" },
  labelLight: { color: "#141726" },
  input: {
    borderWidth: 1.5,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: 10,
    fontSize: 15,
  },
  inputDark: { backgroundColor: "#181D28", borderColor: "#2D3648", color: "#F1F5F9" },
  inputLight: { backgroundColor: "#F4F7FF", borderColor: "#CBD5E1", color: "#141726" },
  textarea: { minHeight: 88, textAlignVertical: "top" },
  inputError: { borderColor: "#EF4444" },
  errorText: { color: "#EF4444", fontSize: 12, fontWeight: "600" },
  pills: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  pill: {
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: "#334155",
    backgroundColor: "#1E293B",
  },
  pillSelected: { borderColor: "#38BDF8", backgroundColor: "#0284C7" },
  pillText: { color: "#CBD5E1", fontWeight: "600", fontSize: 13 },
  pillTextSelected: { color: "#FFFFFF" },
});
