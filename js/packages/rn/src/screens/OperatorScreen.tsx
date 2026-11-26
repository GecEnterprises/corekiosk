import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  Modal,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  View,
} from "react-native";
import type { ActionSpec, DeviceRecord, SettingSpec, SubmissionRecord } from "@corekiosk/client";
import { DynamicForm } from "../components/DynamicForm.js";
import { useKiosk, useOperator } from "../hooks.js";

type OpTab = "controls" | "boards" | "history" | "system";

export interface OperatorScreenProps {
  extraControls?: React.ReactNode;
}

export function OperatorScreen({ extraControls }: OperatorScreenProps) {
  const { state, serverUrl, clientDeviceId, closeOperator, openConnect, unpair } = useKiosk();
  const op = useOperator();
  const [tab, setTab] = useState<OpTab>("controls");
  const [toast, setToast] = useState<string | null>(null);

  // Action parameter modal & cross-platform confirm modal (works on Android, iOS, and Web)
  const [paramAction, setParamAction] = useState<ActionSpec | null>(null);
  const [paramValues, setParamValues] = useState<Record<string, unknown>>({});
  const [confirmDialog, setConfirmDialog] = useState<{
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    onConfirm: () => void;
  } | null>(null);

  // History & System state
  const [historySearch, setHistorySearch] = useState("");
  const [submissions, setSubmissions] = useState<SubmissionRecord[]>([]);
  const [devices, setDevices] = useState<DeviceRecord[]>([]);

  const showToast = useCallback((msg: string) => {
    setToast(msg);
    setTimeout(() => setToast((cur) => (cur === msg ? null : cur)), 2800);
  }, []);

  const loadHistory = useCallback(async () => {
    try {
      const res = await op.fetchSubmissions(historySearch, 100, 0);
      setSubmissions(res.items);
    } catch (err) {
      showToast(err instanceof Error ? err.message : "Failed to load history");
    }
  }, [historySearch, op, showToast]);

  const loadDevices = useCallback(async () => {
    try {
      setDevices(await op.fetchDevices());
    } catch (err) {
      showToast(err instanceof Error ? err.message : "Failed to load devices");
    }
  }, [op, showToast]);

  useEffect(() => {
    if (tab === "history") void loadHistory();
    if (tab === "system") void loadDevices();
  }, [tab, loadHistory, loadDevices]);

  const groupedSettings = useMemo(() => {
    const groups: Array<{ group: string; items: SettingSpec[] }> = [];
    const map = new Map<string, SettingSpec[]>();
    const allValues = { ...state.values, ...state.runtimeValues };
    for (const s of state.settings) {
      if (s.visible_if && allValues[s.visible_if.key] !== s.visible_if.equals) {
        continue;
      }
      const g = s.group || "General";
      if (!map.has(g)) {
        const arr: SettingSpec[] = [];
        map.set(g, arr);
        groups.push({ group: g, items: arr });
      }
      map.get(g)!.push(s);
    }
    return groups;
  }, [state.settings, state.values, state.runtimeValues]);

  const onPatch = async (key: string, val: unknown) => {
    try {
      await op.patchConfig({ [key]: val }, true);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const runAction = async (act: ActionSpec, params: Record<string, unknown> = {}) => {
    try {
      const res = await op.triggerAction(act.id, params);
      showToast(res.message || `${act.label} OK`);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const onPressAction = (act: ActionSpec) => {
    if (act.params?.length) {
      setParamValues({});
      setParamAction(act);
      return;
    }
    if (act.confirm !== "none") {
      setConfirmDialog({
        title: act.label,
        message: act.confirm_text || `${act.label}?`,
        confirmLabel: "Confirm",
        danger: act.style === "danger",
        onConfirm: () => void runAction(act),
      });
      return;
    }
    void runAction(act);
  };

  const confirmDeleteBoardRow = (boardId: string, entryId: string, rank: number) => {
    setConfirmDialog({
      title: "Delete Row",
      message: `Remove #${rank} from display?`,
      confirmLabel: "Delete",
      danger: true,
      onConfirm: async () => {
        try {
          await op.deleteBoardRow(boardId, entryId);
          showToast("Row removed");
        } catch (err) {
          showToast(err instanceof Error ? err.message : String(err));
        }
      },
    });
  };

  const actions = state.schema?.actions ?? [];
  const boards = Object.values(state.boards);
  const wifi = state.snapshot?.wifi;

  return (
    <View style={styles.root}>
      {/* Top Bar */}
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Operator ({op.username ?? ""})</Text>
        <View style={styles.headerRight}>
          <Pressable style={styles.closeBtn} onPress={closeOperator}>
            <Text style={styles.closeText}>Lock & Exit</Text>
          </Pressable>
        </View>
      </View>

      {/* Tabs */}
      <View style={styles.tabs}>
        {(["controls", "boards", "history", "system"] as OpTab[]).map((t) => (
          <Pressable key={t} style={[styles.tabBtn, tab === t && styles.tabBtnActive]} onPress={() => setTab(t)}>
            <Text style={[styles.tabText, tab === t && styles.tabTextActive]}>
              {t.charAt(0).toUpperCase() + t.slice(1)}
            </Text>
          </Pressable>
        ))}
      </View>

      {/* Body */}
      <ScrollView style={styles.scroll} contentContainerStyle={styles.scrollContent}>
        {tab === "controls" ? (
          <>
            {extraControls}
            {groupedSettings.map(({ group, items }) => (
              <View key={group} style={styles.panel}>
                <Text style={styles.panelTitle}>{group}</Text>
                {items.map((spec) => {
                  const val = spec.scope === "runtime" ? state.runtimeValues[spec.key] : state.values[spec.key];
                  return (
                    <View key={spec.key} style={styles.settingRow}>
                      <View style={styles.settingMeta}>
                        <Text style={styles.settingLabel}>{spec.label}</Text>
                        {spec.scope === "runtime" || spec.apply !== "immediate" ? (
                          <Text style={styles.settingTag}>
                            {[spec.scope === "runtime" ? "runtime" : "", spec.apply !== "immediate" ? spec.apply : ""]
                              .filter(Boolean)
                              .join(" / ")}
                          </Text>
                        ) : null}
                      </View>

                      {spec.type === "bool" ? (
                        <Switch value={Boolean(val)} onValueChange={(v) => void onPatch(spec.key, v)} />
                      ) : spec.type === "enum" && spec.options?.length ? (
                        <View style={styles.pills}>
                          {spec.options.map((o) => {
                            const active = val === o.value;
                            return (
                              <Pressable
                                key={o.value}
                                style={[styles.pill, active && styles.pillActive]}
                                onPress={() => void onPatch(spec.key, o.value)}
                              >
                                <Text style={[styles.pillText, active && styles.pillTextActive]}>{o.label}</Text>
                              </Pressable>
                            );
                          })}
                        </View>
                      ) : (
                        <SettingValueInput
                          initialValue={val !== undefined && val !== null ? String(val) : ""}
                          numeric={spec.type === "int" || spec.type === "number"}
                          onCommit={(txt) => {
                            if (spec.type === "int") {
                              const n = parseInt(txt, 10);
                              if (!Number.isNaN(n)) void onPatch(spec.key, n);
                            } else if (spec.type === "number") {
                              const f = parseFloat(txt);
                              if (!Number.isNaN(f)) void onPatch(spec.key, f);
                            } else {
                              void onPatch(spec.key, txt);
                            }
                          }}
                        />
                      )}
                    </View>
                  );
                })}
              </View>
            ))}

            {actions.length > 0 ? (
              <View style={styles.panel}>
                <Text style={styles.panelTitle}>Actions</Text>
                <View style={styles.actionGrid}>
                  {actions.map((act) => (
                    <Pressable
                      key={act.id}
                      onPress={() => onPressAction(act)}
                      style={[
                        styles.actionBtn,
                        act.style === "danger" && styles.actionDanger,
                        act.style === "primary" && styles.actionPrimary,
                      ]}
                    >
                      <Text style={styles.actionBtnText}>{act.label}</Text>
                    </Pressable>
                  ))}
                </View>
              </View>
            ) : null}
          </>
        ) : null}

        {tab === "boards" ? (
          boards.length === 0 ? (
            <Text style={styles.muted}>No boards defined.</Text>
          ) : (
            boards.map((board) => (
              <View key={board.board_id} style={styles.panel}>
                <Text style={styles.panelTitle}>
                  {board.title} ({board.rows.length})
                </Text>
                {board.rows.length === 0 ? (
                  <Text style={styles.muted}>Empty</Text>
                ) : (
                  board.rows.map((r) => (
                    <View key={r.entry_id} style={styles.boardRow}>
                      <Text style={styles.boardRank}>#{r.rank}</Text>
                      <View style={{ flex: 1 }}>
                        <Text style={styles.boardName}>{String(r.display.name ?? r.entry_id)}</Text>
                        <Text style={styles.boardSub}>{r.sort_value}</Text>
                      </View>
                      <Pressable
                        style={styles.deleteBtn}
                        onPress={() => confirmDeleteBoardRow(board.board_id, r.entry_id, r.rank)}
                      >
                        <Text style={styles.deleteText}>Delete</Text>
                      </Pressable>
                    </View>
                  ))
                )}
              </View>
            ))
          )
        ) : null}

        {tab === "history" ? (
          <View style={styles.panel}>
            <View style={styles.searchRow}>
              <TextInput
                value={historySearch}
                onChangeText={setHistorySearch}
                placeholder="Search submissions..."
                placeholderTextColor="#64748B"
                onSubmitEditing={() => void loadHistory()}
                style={[styles.smallInput, { flex: 1 }]}
              />
              <Pressable style={styles.actionBtn} onPress={() => void loadHistory()}>
                <Text style={styles.actionBtnText}>Search</Text>
              </Pressable>
            </View>
            {submissions.length === 0 ? (
              <Text style={styles.muted}>No submissions yet.</Text>
            ) : (
              submissions.map((sub) => (
                <View key={sub.id} style={styles.historyRow}>
                  <View style={{ flex: 1, gap: 2 }}>
                    <Text style={styles.boardName}>{String(sub.payload.name ?? sub.id)}</Text>
                    <Text style={styles.muted}>{JSON.stringify(sub.payload)}</Text>
                    <Text style={styles.settingTag}>
                      {new Date(sub.created_at_ms).toLocaleString()} ({sub.status})
                    </Text>
                  </View>
                </View>
              ))
            )}
          </View>
        ) : null}

        {tab === "system" ? (
          <>
            <View style={styles.panel}>
              <Text style={styles.panelTitle}>Connection</Text>
              <Text style={styles.settingLabel}>Server: {serverUrl}</Text>
              <Text style={styles.settingLabel}>
                Server Wi-Fi: {wifi?.connected && wifi.ssid ? wifi.ssid : "Not connected"}
              </Text>
              <Text style={styles.settingLabel}>Device Key: {clientDeviceId}</Text>
              <View style={styles.actionGrid}>
                <Pressable style={styles.actionBtn} onPress={openConnect}>
                  <Text style={styles.actionBtnText}>Change Server IP</Text>
                </Pressable>
                <Pressable
                  style={[styles.actionBtn, styles.actionDanger]}
                  onPress={() =>
                    setConfirmDialog({
                      title: "Unpair Device",
                      message: "Remove pinned server?",
                      confirmLabel: "Unpair",
                      danger: true,
                      onConfirm: () => void unpair(),
                    })
                  }
                >
                  <Text style={styles.actionBtnText}>Unpair</Text>
                </Pressable>
              </View>
            </View>

            <View style={styles.panel}>
              <Text style={styles.panelTitle}>Paired Devices ({devices.length})</Text>
              {devices.map((d) => (
                <View key={d.device_id} style={styles.historyRow}>
                  <View style={{ flex: 1 }}>
                    <Text style={styles.boardName}>
                      [{d.role.toUpperCase()}] {d.device_name}
                    </Text>
                    <Text style={styles.settingTag}>
                      key={d.device_id} {d.model || d.device_type} {d.last_ip || ""}
                    </Text>
                  </View>
                </View>
              ))}
            </View>
          </>
        ) : null}
      </ScrollView>

      {toast ? (
        <View style={styles.toast}>
          <Text style={styles.toastText}>{toast}</Text>
        </View>
      ) : null}

      <Modal visible={Boolean(paramAction)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalCard}>
            <Text style={styles.headerTitle}>{paramAction?.label}</Text>
            {paramAction?.params ? (
              <DynamicForm
                fields={paramAction.params}
                values={paramValues}
                onChange={(k, v) => setParamValues((prev) => ({ ...prev, [k]: v }))}
              />
            ) : null}
            <View style={styles.headerRight}>
              <Pressable style={styles.closeBtn} onPress={() => setParamAction(null)}>
                <Text style={styles.closeText}>Cancel</Text>
              </Pressable>
              <Pressable
                style={[styles.actionBtn, styles.actionPrimary]}
                onPress={() => {
                  const act = paramAction;
                  setParamAction(null);
                  if (act) void runAction(act, paramValues);
                }}
              >
                <Text style={styles.actionBtnText}>Run</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>
      <Modal visible={Boolean(confirmDialog)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalCard}>
            <Text style={styles.headerTitle}>{confirmDialog?.title}</Text>
            <Text style={styles.settingLabel}>{confirmDialog?.message}</Text>
            <View style={styles.headerRight}>
              <Pressable style={styles.closeBtn} onPress={() => setConfirmDialog(null)}>
                <Text style={styles.closeText}>Cancel</Text>
              </Pressable>
              <Pressable
                style={[styles.actionBtn, confirmDialog?.danger ? styles.actionDanger : styles.actionPrimary]}
                onPress={() => {
                  const fn = confirmDialog?.onConfirm;
                  setConfirmDialog(null);
                  fn?.();
                }}
              >
                <Text style={styles.actionBtnText}>{confirmDialog?.confirmLabel ?? "Confirm"}</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>
    </View>
  );
}

function SettingValueInput({
  initialValue,
  numeric,
  onCommit,
}: {
  initialValue: string;
  numeric: boolean;
  onCommit: (txt: string) => void;
}) {
  const [text, setText] = useState(initialValue);
  useEffect(() => {
    setText(initialValue);
  }, [initialValue]);
  return (
    <TextInput
      value={text}
      onChangeText={setText}
      onBlur={() => {
        if (text.trim() !== initialValue) onCommit(text.trim());
      }}
      onSubmitEditing={() => onCommit(text.trim())}
      keyboardType={numeric ? "numeric" : "default"}
      style={styles.smallInput}
    />
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: "#0F131A" },
  header: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    paddingHorizontal: 18,
    paddingVertical: 14,
    backgroundColor: "#161B24",
    borderBottomWidth: 1,
    borderBottomColor: "#252D3D",
  },
  headerTitle: { color: "#F1F5F9", fontSize: 18, fontWeight: "700" },
  headerRight: { flexDirection: "row", gap: 10 },
  closeBtn: {
    backgroundColor: "#1E2634",
    borderWidth: 1,
    borderColor: "#334155",
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderRadius: 8,
  },
  closeText: { color: "#E2E8F0", fontWeight: "600" },
  tabs: {
    flexDirection: "row",
    backgroundColor: "#161B24",
    borderBottomWidth: 1,
    borderBottomColor: "#252D3D",
    paddingHorizontal: 12,
  },
  tabBtn: { paddingHorizontal: 16, paddingVertical: 12, borderBottomWidth: 2, borderBottomColor: "transparent" },
  tabBtnActive: { borderBottomColor: "#38BDF8" },
  tabText: { color: "#94A3B8", fontWeight: "600", fontSize: 14 },
  tabTextActive: { color: "#F8FAFC" },
  scroll: { flex: 1 },
  scrollContent: { padding: 16, gap: 14, paddingBottom: 40 },
  panel: {
    backgroundColor: "#161B24",
    borderColor: "#252D3D",
    borderWidth: 1,
    borderRadius: 12,
    padding: 16,
    gap: 12,
  },
  panelTitle: { color: "#94A3B8", fontSize: 12, fontWeight: "700", textTransform: "uppercase" },
  settingRow: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    gap: 12,
    paddingVertical: 6,
    borderBottomWidth: 1,
    borderBottomColor: "#1E2534",
  },
  settingMeta: { flex: 1 },
  settingLabel: { color: "#E2E8F0", fontSize: 14, fontWeight: "600" },
  settingTag: { color: "#64748B", fontSize: 12 },
  pills: { flexDirection: "row", flexWrap: "wrap", gap: 6, justifyContent: "flex-end" },
  pill: {
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 7,
    backgroundColor: "#1E2634",
    borderWidth: 1,
    borderColor: "#334155",
  },
  pillActive: { backgroundColor: "#0284C7", borderColor: "#38BDF8" },
  pillText: { color: "#CBD5E1", fontSize: 12, fontWeight: "600" },
  pillTextActive: { color: "#FFFFFF" },
  smallInput: {
    minWidth: 110,
    backgroundColor: "#10141C",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 8,
    paddingHorizontal: 10,
    paddingVertical: 7,
    color: "#F8FAFC",
    fontSize: 14,
  },
  actionGrid: { flexDirection: "row", flexWrap: "wrap", gap: 10 },
  actionBtn: {
    backgroundColor: "#1E2634",
    borderColor: "#334155",
    borderWidth: 1,
    paddingHorizontal: 14,
    paddingVertical: 10,
    borderRadius: 8,
  },
  actionPrimary: { backgroundColor: "#0284C7", borderColor: "#38BDF8" },
  actionDanger: { backgroundColor: "#991B1B", borderColor: "#EF4444" },
  actionBtnText: { color: "#F8FAFC", fontWeight: "700", fontSize: 13 },
  boardRow: {
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    paddingVertical: 8,
    borderBottomWidth: 1,
    borderBottomColor: "#1E2534",
  },
  boardRank: { color: "#38BDF8", fontWeight: "800", fontSize: 15, width: 34 },
  boardName: { color: "#F1F5F9", fontWeight: "700", fontSize: 14 },
  boardSub: { color: "#94A3B8", fontSize: 13 },
  deleteBtn: {
    backgroundColor: "#7F1D1D",
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 6,
  },
  deleteText: { color: "#FCA5A5", fontWeight: "700", fontSize: 12 },
  searchRow: { flexDirection: "row", gap: 8, alignItems: "center" },
  historyRow: {
    flexDirection: "row",
    paddingVertical: 8,
    borderBottomWidth: 1,
    borderBottomColor: "#1E2534",
  },
  muted: { color: "#94A3B8", fontSize: 13 },
  toast: {
    position: "absolute",
    bottom: 20,
    right: 20,
    backgroundColor: "#1E293B",
    borderColor: "#38BDF8",
    borderWidth: 1,
    paddingHorizontal: 14,
    paddingVertical: 10,
    borderRadius: 10,
  },
  toastText: { color: "#F8FAFC", fontWeight: "600" },
  modalBackdrop: {
    flex: 1,
    backgroundColor: "rgba(0,0,0,0.65)",
    justifyContent: "center",
    alignItems: "center",
    padding: 24,
  },
  modalCard: {
    width: "100%",
    maxWidth: 440,
    backgroundColor: "#161B24",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 14,
    padding: 20,
    gap: 14,
  },
});
