import React from "react";
import { Image, Pressable, StyleSheet, Switch, Text, TextInput, View } from "react-native";
import type { CatalogView, FormField } from "@corekiosk/client";

export interface DynamicFormProps {
  fields: FormField[];
  values: Record<string, unknown>;
  errors?: Record<string, string>;
  onChange(key: string, value: unknown): void;
  disabled?: boolean;
  dark?: boolean;
  resolveAssetUrl?: (assetId: string) => string;
  onPickImage?: (currentAssetId: string, onSelected: (newAssetId: string) => void) => void;
  catalogs?: Record<string, CatalogView>;
}

export function DynamicForm({
  fields,
  values,
  errors = {},
  onChange,
  disabled,
  dark = true,
  resolveAssetUrl,
  onPickImage,
  catalogs,
}: DynamicFormProps) {
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
            ) : field.type === "image" ? (
              <View style={styles.imageRow}>
                {typeof val === "string" && val && resolveAssetUrl ? (
                  <Image source={{ uri: resolveAssetUrl(val) }} style={styles.thumb} resizeMode="contain" />
                ) : (
                  <View style={[styles.thumb, styles.thumbEmpty]}>
                    <Text style={styles.thumbEmptyText}>No Img</Text>
                  </View>
                )}
                {onPickImage ? (
                  <Pressable
                    disabled={disabled}
                    style={styles.uploadBtn}
                    onPress={() => onPickImage(typeof val === "string" ? val : "", (id) => onChange(field.key, id))}
                  >
                    <Text style={styles.uploadBtnText}>{val ? "Change Image" : "Upload Image"}</Text>
                  </Pressable>
                ) : null}
              </View>
            ) : field.type === "catalog_ref" && field.catalog_id && catalogs?.[field.catalog_id] ? (
              <View style={styles.pills}>
                {catalogs[field.catalog_id].entries.map((ent) => {
                  const selected = val === ent.id;
                  const title = String(ent.fields.name ?? ent.fields.title ?? ent.id);
                  return (
                    <Pressable
                      key={ent.id}
                      disabled={disabled}
                      onPress={() => onChange(field.key, ent.id)}
                      style={[styles.pill, selected && styles.pillSelected]}
                    >
                      <Text style={[styles.pillText, selected && styles.pillTextSelected]}>{title}</Text>
                    </Pressable>
                  );
                })}
              </View>
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
  imageRow: { flexDirection: "row", alignItems: "center", gap: 12 },
  thumb: {
    width: 60,
    height: 60,
    borderRadius: 8,
    backgroundColor: "#10141C",
    borderWidth: 1,
    borderColor: "#2D3648",
  },
  thumbEmpty: { alignItems: "center", justifyContent: "center" },
  thumbEmptyText: { color: "#64748B", fontSize: 11, fontWeight: "600" },
  uploadBtn: {
    backgroundColor: "#0284C7",
    borderColor: "#38BDF8",
    borderWidth: 1,
    paddingHorizontal: 14,
    paddingVertical: 9,
    borderRadius: 8,
  },
  uploadBtnText: { color: "#FFFFFF", fontWeight: "700", fontSize: 13 },
});
