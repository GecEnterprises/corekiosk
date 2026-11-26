import React from "react";
import { Image, Pressable, StyleSheet, Switch, Text, TextInput, View } from "react-native";
import type { CatalogView, FormField } from "@corekiosk/client";
import { OpColors, OpFonts, OpMetrics } from "../operatorTheme.js";

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
            <Text style={styles.label}>
              {field.label}
              {field.required ? " *" : ""}
            </Text>
            {field.help ? <Text style={styles.helpText}>{field.help}</Text> : null}

            {field.type === "bool" ? (
              <View style={styles.switchRow}>
                <Switch
                  value={Boolean(val)}
                  onValueChange={(v) => onChange(field.key, v)}
                  disabled={disabled}
                  trackColor={{ false: OpColors.lineStrong, true: OpColors.selectedBg }}
                />
                <Text style={styles.switchState}>{val ? "Enabled" : "Disabled"}</Text>
              </View>
            ) : field.type === "image" ? (
              <View style={styles.imageRow}>
                {typeof val === "string" && val && resolveAssetUrl ? (
                  <Image source={{ uri: resolveAssetUrl(val) }} style={styles.thumb} resizeMode="contain" />
                ) : (
                  <View style={[styles.thumb, styles.thumbEmpty]}>
                    <Text style={styles.thumbEmptyText}>None</Text>
                  </View>
                )}
                {onPickImage ? (
                  <Pressable
                    disabled={disabled}
                    style={({ pressed }) => [styles.uploadBtn, pressed && styles.uploadBtnPressed]}
                    onPress={() => onPickImage(typeof val === "string" ? val : "", (id) => onChange(field.key, id))}
                  >
                    <Text style={styles.uploadBtnText}>{val ? "Replace Image…" : "Choose Image…"}</Text>
                  </Pressable>
                ) : null}
              </View>
            ) : field.type === "catalog_ref" && field.catalog_id && catalogs?.[field.catalog_id] ? (
              <View style={styles.segmentWrap}>
                {catalogs[field.catalog_id].entries.map((ent) => {
                  const selected = val === ent.id;
                  const title = String(ent.fields.name ?? ent.fields.title ?? ent.id);
                  return (
                    <Pressable
                      key={ent.id}
                      disabled={disabled}
                      onPress={() => onChange(field.key, ent.id)}
                      style={[styles.segmentBtn, selected && styles.segmentBtnSelected]}
                    >
                      <Text style={[styles.segmentText, selected && styles.segmentTextSelected]}>{title}</Text>
                    </Pressable>
                  );
                })}
              </View>
            ) : field.type === "enum" && field.options?.length ? (
              <View style={styles.segmentWrap}>
                {field.options.map((opt) => {
                  const selected = val === opt.value;
                  return (
                    <Pressable
                      key={opt.value}
                      disabled={disabled}
                      onPress={() => onChange(field.key, opt.value)}
                      style={[styles.segmentBtn, selected && styles.segmentBtnSelected]}
                    >
                      <Text style={[styles.segmentText, selected && styles.segmentTextSelected]}>{opt.label}</Text>
                    </Pressable>
                  );
                })}
              </View>
            ) : (
              <TextInput
                value={val !== undefined && val !== null ? String(val) : ""}
                onChangeText={(txt) => onChange(field.key, field.type === "number" ? Number(txt) : txt)}
                placeholder={field.placeholder ?? field.label}
                placeholderTextColor={OpColors.inkMuted}
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
  container: { gap: 18 },
  field: { gap: 8 },
  label: { fontSize: 15, fontWeight: "600", color: OpColors.ink, fontFamily: OpFonts.sans },
  helpText: { fontSize: 14, color: OpColors.inkSecondary, fontFamily: OpFonts.sans },
  switchRow: { flexDirection: "row", alignItems: "center", gap: 12, minHeight: 44 },
  switchState: { fontSize: 15, color: OpColors.inkSecondary, fontFamily: OpFonts.sans },
  input: {
    minHeight: 50,
    backgroundColor: OpColors.ground,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 16,
    paddingVertical: 12,
    fontSize: 17,
    color: OpColors.ink,
    fontFamily: OpFonts.sans,
  },
  textarea: { minHeight: 96, textAlignVertical: "top" },
  inputError: { borderColor: OpColors.danger },
  errorText: { color: OpColors.danger, fontSize: 14, fontWeight: "600", fontFamily: OpFonts.sans },
  segmentWrap: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  segmentBtn: {
    minHeight: 44,
    paddingHorizontal: 16,
    paddingVertical: 10,
    borderRadius: OpMetrics.radiusSm,
    backgroundColor: OpColors.recessed,
    justifyContent: "center",
  },
  segmentBtnSelected: { backgroundColor: OpColors.selectedBg },
  segmentText: { color: OpColors.ink, fontWeight: "600", fontSize: 15, fontFamily: OpFonts.sans },
  segmentTextSelected: { color: OpColors.selectedFg },
  imageRow: { flexDirection: "row", alignItems: "center", gap: 16 },
  thumb: {
    width: 76,
    height: 76,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.ground,
    borderWidth: 1,
    borderColor: OpColors.line,
  },
  thumbEmpty: { alignItems: "center", justifyContent: "center" },
  thumbEmptyText: { color: OpColors.inkMuted, fontSize: 13, fontWeight: "600", fontFamily: OpFonts.sans },
  uploadBtn: {
    minHeight: 46,
    backgroundColor: OpColors.recessed,
    paddingHorizontal: 18,
    paddingVertical: 12,
    borderRadius: OpMetrics.radiusMd,
    justifyContent: "center",
  },
  uploadBtnPressed: { backgroundColor: OpColors.hover },
  uploadBtnText: { color: OpColors.ink, fontWeight: "600", fontSize: 15, fontFamily: OpFonts.sans },
});
