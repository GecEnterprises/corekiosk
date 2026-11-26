import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  Image,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  View,
} from "react-native";
import type {
  ActionSpec,
  AssetMeta,
  CatalogEntry,
  CatalogSpec,
  DeviceRecord,
  RuleEntry,
  RulesetSpec,
  SettingSpec,
  SubmissionRecord,
} from "@corekiosk/client";
import { DynamicForm } from "../components/DynamicForm.js";
import { useKiosk, useOperator } from "../hooks.js";

type OpTab = "controls" | "prizes" | "boards" | "history" | "system";

export interface OperatorScreenProps {
  extraControls?: React.ReactNode;
}

function catalogEntryTitle(spec: CatalogSpec, entry: CatalogEntry): string {
  const key = spec.title_field || spec.fields.find((f) => f.type === "text")?.key || "";
  const raw = key ? entry.fields[key] : undefined;
  return raw !== undefined && raw !== null && String(raw).trim() !== "" ? String(raw) : entry.id;
}

function catalogEntryImageId(spec: CatalogSpec, entry: CatalogEntry): string {
  const key = spec.image_field || spec.fields.find((f) => f.type === "image")?.key || "";
  const raw = key ? entry.fields[key] : undefined;
  return typeof raw === "string" ? raw.trim() : "";
}

export function OperatorScreen({ extraControls }: OperatorScreenProps) {
  const { state, serverUrl, clientDeviceId, closeOperator, openConnect, unpair } = useKiosk();
  const op = useOperator();
  const hasCatalogsOrRules =
    (state.schema?.catalogs?.length ?? 0) > 0 || (state.schema?.rulesets?.length ?? 0) > 0;
  const [tab, setTab] = useState<OpTab>("controls");
  const [toast, setToast] = useState<string | null>(null);

  // Action parameter modal & cross-platform confirm modal
  const [paramAction, setParamAction] = useState<ActionSpec | null>(null);
  const [paramValues, setParamValues] = useState<Record<string, unknown>>({});
  const [confirmDialog, setConfirmDialog] = useState<{
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    onConfirm: () => void;
  } | null>(null);

  // Catalog entry add/edit modal & asset picker modal
  const [editingCatalog, setEditingCatalog] = useState<{
    spec: CatalogSpec;
    entry: CatalogEntry | null;
  } | null>(null);
  const [entryDraftId, setEntryDraftId] = useState("");
  const [entryDraftFields, setEntryDraftFields] = useState<Record<string, unknown>>({});

  const [assetPicker, setAssetPicker] = useState<{
    currentId: string;
    onSelect: (assetId: string) => void;
  } | null>(null);
  const [recentAssets, setRecentAssets] = useState<AssetMeta[]>([]);
  const [uploadingAsset, setUploadingAsset] = useState(false);

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

  const openAssetPicker = useCallback(
    (currentId: string, onSelect: (assetId: string) => void) => {
      setAssetPicker({ currentId, onSelect });
      void op
        .fetchAssets()
        .then(setRecentAssets)
        .catch(() => {});
    },
    [op]
  );

  const triggerWebFileUpload = useCallback(
    (onUploaded: (assetId: string) => void) => {
      if (Platform.OS === "web" && typeof document !== "undefined") {
        const input = document.createElement("input");
        input.type = "file";
        input.accept = "image/png,image/jpeg,image/webp,image/gif";
        input.onchange = async () => {
          const file = input.files?.[0];
          if (!file) return;
          setUploadingAsset(true);
          try {
            const meta = await op.uploadAssetFile(file, file.name);
            setRecentAssets((prev) => [meta, ...prev.filter((a) => a.id !== meta.id)]);
            onUploaded(meta.id);
            showToast(`Uploaded ${meta.filename || meta.id}`);
          } catch (err) {
            showToast(err instanceof Error ? err.message : String(err));
          } finally {
            setUploadingAsset(false);
          }
        };
        input.click();
        return;
      }
      showToast("Select from server assets below or upload via Kiosk Launcher / Web.");
    },
    [op, showToast]
  );

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

  const openCatalogEntryModal = (spec: CatalogSpec, entry: CatalogEntry | null) => {
    setEditingCatalog({ spec, entry });
    setEntryDraftId("");
    setEntryDraftFields(entry ? { ...entry.fields } : {});
  };

  const saveCatalogEntryModal = async () => {
    if (!editingCatalog) return;
    const { spec, entry } = editingCatalog;
    try {
      if (entry) {
        await op.patchCatalogEntry(spec.id, entry.id, { fields: entryDraftFields });
        showToast(`Updated ${catalogEntryTitle(spec, entry)}`);
      } else {
        await op.addCatalogEntry(spec.id, {
          id: entryDraftId.trim() || undefined,
          enabled: true,
          fields: entryDraftFields,
        });
        showToast(`Added entry to ${spec.title}`);
      }
      setEditingCatalog(null);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const actions = state.schema?.actions ?? [];
  const catalogSpecs = state.schema?.catalogs ?? [];
  const rulesetSpecs = state.schema?.rulesets ?? [];
  const boards = Object.values(state.boards);
  const wifi = state.snapshot?.wifi;

  const visibleTabs: Array<{ id: OpTab; label: string }> = [
    { id: "controls", label: "Controls" },
    ...(hasCatalogsOrRules ? [{ id: "prizes" as OpTab, label: "Prizes & Rules" }] : []),
    { id: "boards", label: "Boards" },
    { id: "history", label: "History" },
    { id: "system", label: "System" },
  ];

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
        {visibleTabs.map((t) => (
          <Pressable key={t.id} style={[styles.tabBtn, tab === t.id && styles.tabBtnActive]} onPress={() => setTab(t.id)}>
            <Text style={[styles.tabText, tab === t.id && styles.tabTextActive]}>{t.label}</Text>
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
                      ) : spec.type === "image" ? (
                        <View style={styles.inlineImageRow}>
                          {typeof val === "string" && val ? (
                            <Image source={{ uri: op.assetUrl(val) }} style={styles.smallThumb} resizeMode="contain" />
                          ) : null}
                          <Pressable
                            style={styles.pill}
                            onPress={() =>
                              openAssetPicker(typeof val === "string" ? val : "", (id) => void onPatch(spec.key, id))
                            }
                          >
                            <Text style={styles.pillText}>{val ? "Change" : "Upload"}</Text>
                          </Pressable>
                        </View>
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

        {tab === "prizes" ? (
          <>
            {rulesetSpecs.map((rSpec) => (
              <RulesetEditorPanel
                key={rSpec.id}
                spec={rSpec}
                rules={state.rulesets[rSpec.id]?.rules ?? rSpec.defaults ?? []}
                catalogSpec={catalogSpecs.find((c) => c.id === rSpec.catalog_id)}
                catalogEntries={rSpec.catalog_id ? state.catalogs[rSpec.catalog_id]?.entries ?? [] : []}
                assetUrl={op.assetUrl}
                onSave={async (nextRules) => {
                  try {
                    await op.saveRuleset(rSpec.id, nextRules);
                    showToast(`Saved ${rSpec.title}`);
                  } catch (err) {
                    showToast(err instanceof Error ? err.message : String(err));
                  }
                }}
              />
            ))}

            {catalogSpecs.map((cSpec) => {
              const entries = state.catalogs[cSpec.id]?.entries ?? cSpec.defaults ?? [];
              const imgKey = cSpec.image_field || cSpec.fields.find((f) => f.type === "image")?.key;
              return (
                <View key={cSpec.id} style={styles.panel}>
                  <View style={styles.panelHeaderRow}>
                    <View style={{ flex: 1 }}>
                      <Text style={styles.panelTitle}>
                        {cSpec.title} ({entries.length})
                      </Text>
                      {cSpec.help ? <Text style={styles.settingTag}>{cSpec.help}</Text> : null}
                    </View>
                    {cSpec.allow_add_delete ? (
                      <Pressable
                        style={[styles.actionBtn, styles.actionPrimary]}
                        onPress={() => openCatalogEntryModal(cSpec, null)}
                      >
                        <Text style={styles.actionBtnText}>+ Add Item</Text>
                      </Pressable>
                    ) : null}
                  </View>

                  {entries.map((entry) => {
                    const imgId = catalogEntryImageId(cSpec, entry);
                    const title = catalogEntryTitle(cSpec, entry);
                    return (
                      <View key={entry.id} style={styles.catalogCard}>
                        <Pressable
                          onPress={() => {
                            if (!imgKey) {
                              openCatalogEntryModal(cSpec, entry);
                              return;
                            }
                            openAssetPicker(imgId, (newAssetId) => {
                              void op
                                .patchCatalogEntry(cSpec.id, entry.id, { fields: { [imgKey]: newAssetId } })
                                .then(() => showToast(`Updated image for ${title}`))
                                .catch((err) => showToast(err instanceof Error ? err.message : String(err)));
                            });
                          }}
                        >
                          {imgId ? (
                            <Image source={{ uri: op.assetUrl(imgId) }} style={styles.catalogImg} resizeMode="contain" />
                          ) : (
                            <View style={[styles.catalogImg, styles.catalogImgEmpty]}>
                              <Text style={styles.settingTag}>+ Image</Text>
                            </View>
                          )}
                        </Pressable>

                        <View style={{ flex: 1, gap: 3 }}>
                          <Text style={styles.boardName}>{title}</Text>
                          <Text style={styles.settingTag}>
                            id: {entry.id} {!entry.enabled ? " • INACTIVE" : ""}
                          </Text>
                        </View>

                        <View style={styles.catalogActions}>
                          <Switch
                            value={entry.enabled}
                            onValueChange={(v) =>
                              void op
                                .patchCatalogEntry(cSpec.id, entry.id, { enabled: v })
                                .catch((err) => showToast(err instanceof Error ? err.message : String(err)))
                            }
                          />
                          <Pressable style={styles.pill} onPress={() => openCatalogEntryModal(cSpec, entry)}>
                            <Text style={styles.pillText}>Edit</Text>
                          </Pressable>
                          {cSpec.allow_add_delete ? (
                            <Pressable
                              style={styles.deleteBtn}
                              onPress={() =>
                                setConfirmDialog({
                                  title: "Delete Item",
                                  message: `Delete "${title}" from ${cSpec.title}?`,
                                  confirmLabel: "Delete",
                                  danger: true,
                                  onConfirm: () =>
                                    void op
                                      .deleteCatalogEntry(cSpec.id, entry.id)
                                      .then(() => showToast(`Deleted ${title}`))
                                      .catch((err) => showToast(err instanceof Error ? err.message : String(err))),
                                })
                              }
                            >
                              <Text style={styles.deleteText}>Delete</Text>
                            </Pressable>
                          ) : null}
                        </View>
                      </View>
                    );
                  })}
                </View>
              );
            })}
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
                        <Text style={styles.boardSub}>
                          {r.sort_value}
                          {r.display.reward ? ` • ${String(r.display.reward)}` : ""}
                        </Text>
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
                    <Text style={styles.boardName}>
                      {String(sub.payload.name ?? sub.id)}
                      {sub.outcome?.reward_title ? ` — Won: ${String(sub.outcome.reward_title)}` : ""}
                    </Text>
                    <Text style={styles.muted}>{JSON.stringify(sub.payload)}</Text>
                    <Text style={styles.settingTag}>
                      {new Date(sub.created_at_ms).toLocaleString()} ({sub.status}
                      {sub.outcome?.score !== undefined ? `, score ${String(sub.outcome.score)}` : ""})
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

      {/* Catalog Entry Add/Edit Modal */}
      <Modal visible={Boolean(editingCatalog)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalCard}>
            <Text style={styles.headerTitle}>
              {editingCatalog?.entry ? `Edit ${editingCatalog.spec.title}` : `Add to ${editingCatalog?.spec.title ?? ""}`}
            </Text>
            {editingCatalog && !editingCatalog.entry ? (
              <View style={{ gap: 5 }}>
                <Text style={styles.settingLabel}>Slug / ID (optional)</Text>
                <TextInput
                  value={entryDraftId}
                  onChangeText={setEntryDraftId}
                  placeholder="e.g. sticker_pack"
                  placeholderTextColor="#64748B"
                  autoCapitalize="none"
                  style={styles.smallInput}
                />
              </View>
            ) : null}
            {editingCatalog ? (
              <DynamicForm
                fields={editingCatalog.spec.fields}
                values={entryDraftFields}
                onChange={(k, v) => setEntryDraftFields((prev) => ({ ...prev, [k]: v }))}
                resolveAssetUrl={op.assetUrl}
                onPickImage={openAssetPicker}
                catalogs={state.catalogs}
              />
            ) : null}
            <View style={styles.headerRight}>
              <Pressable style={styles.closeBtn} onPress={() => setEditingCatalog(null)}>
                <Text style={styles.closeText}>Cancel</Text>
              </Pressable>
              <Pressable style={[styles.actionBtn, styles.actionPrimary]} onPress={() => void saveCatalogEntryModal()}>
                <Text style={styles.actionBtnText}>Save</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>

      {/* Asset Upload / Gallery Picker Modal */}
      <Modal visible={Boolean(assetPicker)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={[styles.modalCard, { maxWidth: 520 }]}>
            <Text style={styles.headerTitle}>Choose or Upload Image</Text>
            {Platform.OS === "web" ? (
              <Pressable
                disabled={uploadingAsset}
                style={[styles.actionBtn, styles.actionPrimary, { alignSelf: "flex-start" }]}
                onPress={() =>
                  triggerWebFileUpload((newId) => {
                    const cb = assetPicker?.onSelect;
                    setAssetPicker(null);
                    cb?.(newId);
                  })
                }
              >
                <Text style={styles.actionBtnText}>
                  {uploadingAsset ? "Uploading..." : "Upload PNG / JPG / WebP from Device"}
                </Text>
              </Pressable>
            ) : (
              <Text style={styles.settingTag}>
                Tip: Upload new files from Companion Web or the Kiosk PC Launcher, then tap to select below.
              </Text>
            )}

            <Text style={styles.panelTitle}>Server Media Library ({recentAssets.length})</Text>
            <ScrollView style={{ maxHeight: 260 }} contentContainerStyle={styles.assetGrid}>
              {recentAssets.map((a) => {
                const selected = assetPicker?.currentId === a.id;
                return (
                  <Pressable
                    key={a.id}
                    style={[styles.assetTile, selected && styles.assetTileSelected]}
                    onPress={() => {
                      const cb = assetPicker?.onSelect;
                      setAssetPicker(null);
                      cb?.(a.id);
                    }}
                  >
                    <Image source={{ uri: op.assetUrl(a.id) }} style={styles.assetTileImg} resizeMode="contain" />
                    <Text numberOfLines={1} style={styles.assetTileLabel}>
                      {a.filename || a.seed_key || a.id.slice(0, 10)}
                    </Text>
                  </Pressable>
                );
              })}
            </ScrollView>

            <View style={[styles.headerRight, { justifyContent: "flex-end" }]}>
              <Pressable style={styles.closeBtn} onPress={() => setAssetPicker(null)}>
                <Text style={styles.closeText}>Close</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>

      <Modal visible={Boolean(paramAction)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalCard}>
            <Text style={styles.headerTitle}>{paramAction?.label}</Text>
            {paramAction?.params ? (
              <DynamicForm
                fields={paramAction.params}
                values={paramValues}
                onChange={(k, v) => setParamValues((prev) => ({ ...prev, [k]: v }))}
                resolveAssetUrl={op.assetUrl}
                onPickImage={openAssetPicker}
                catalogs={state.catalogs}
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

function RulesetEditorPanel({
  spec,
  rules,
  catalogSpec,
  catalogEntries,
  assetUrl,
  onSave,
}: {
  spec: RulesetSpec;
  rules: RuleEntry[];
  catalogSpec?: CatalogSpec;
  catalogEntries: CatalogEntry[];
  assetUrl: (id?: string | null) => string;
  onSave: (nextRules: RuleEntry[]) => Promise<void>;
}) {
  const sortedRules = useMemo(() => {
    if (spec.kind !== "score_tiers") return rules;
    return [...rules].sort((a, b) => b.min_score - a.min_score);
  }, [rules, spec.kind]);

  const updateRule = (ruleId: string, patch: Partial<RuleEntry>) => {
    const next = sortedRules.map((r) => (r.id === ruleId ? { ...r, ...patch } : r));
    void onSave(next);
  };

  const addTier = () => {
    const highest = sortedRules[0]?.min_score ?? 0;
    const defaultEntryId = catalogEntries[0]?.id ?? "";
    const next: RuleEntry[] = [
      ...sortedRules,
      {
        id: `tier_${Date.now().toString(36)}`,
        label: `Tier ${sortedRules.length + 1}`,
        enabled: true,
        min_score: highest + 100,
        catalog_id: spec.catalog_id,
        entry_id: defaultEntryId,
      },
    ];
    void onSave(next);
  };

  const removeTier = (ruleId: string) => {
    void onSave(sortedRules.filter((r) => r.id !== ruleId));
  };

  return (
    <View style={styles.panel}>
      <View style={styles.panelHeaderRow}>
        <View style={{ flex: 1 }}>
          <Text style={styles.panelTitle}>{spec.title}</Text>
          <Text style={styles.settingTag}>
            {spec.help || "Adjust minimum score thresholds per tier. Ranges update automatically with zero gaps."}
          </Text>
        </View>
        {spec.allow_add_delete ? (
          <Pressable style={styles.actionBtn} onPress={addTier}>
            <Text style={styles.actionBtnText}>+ Add Tier</Text>
          </Pressable>
        ) : null}
      </View>

      {sortedRules.map((rule, idx) => {
        const upper = idx > 0 ? Math.max(rule.min_score, sortedRules[idx - 1].min_score - 1) : null;
        const rangeLabel = upper === null ? `${rule.min_score}+ pts` : `${rule.min_score} – ${upper} pts`;
        const selectedEntry = catalogEntries.find((e) => e.id === rule.entry_id);
        const imgId = selectedEntry && catalogSpec ? catalogEntryImageId(catalogSpec, selectedEntry) : "";

        return (
          <View key={rule.id} style={styles.tierCard}>
            <View style={styles.tierTopRow}>
              <View style={styles.tierBadge}>
                <Text style={styles.tierBadgeText}>TIER {idx + 1}</Text>
              </View>
              <Text style={styles.tierRangeText}>{rangeLabel}</Text>
              <View style={{ flex: 1 }} />
              <Text style={styles.settingTag}>Min Score:</Text>
              <SettingValueInput
                initialValue={String(rule.min_score)}
                numeric
                onCommit={(txt) => {
                  const n = parseFloat(txt);
                  if (!Number.isNaN(n)) updateRule(rule.id, { min_score: Math.max(0, n) });
                }}
              />
              {spec.allow_add_delete && sortedRules.length > 1 ? (
                <Pressable style={styles.deleteBtn} onPress={() => removeTier(rule.id)}>
                  <Text style={styles.deleteText}>×</Text>
                </Pressable>
              ) : null}
            </View>

            {catalogSpec && catalogEntries.length > 0 ? (
              <View style={styles.tierPrizeRow}>
                {imgId ? <Image source={{ uri: assetUrl(imgId) }} style={styles.smallThumb} resizeMode="contain" /> : null}
                <View style={[styles.pills, { justifyContent: "flex-start", flex: 1 }]}>
                  {catalogEntries.map((ce) => {
                    const active = rule.entry_id === ce.id;
                    const label = catalogEntryTitle(catalogSpec, ce) + (ce.enabled ? "" : " (off)");
                    return (
                      <Pressable
                        key={ce.id}
                        style={[styles.pill, active && styles.pillActive]}
                        onPress={() => updateRule(rule.id, { entry_id: ce.id })}
                      >
                        <Text style={[styles.pillText, active && styles.pillTextActive]}>{label}</Text>
                      </Pressable>
                    );
                  })}
                </View>
              </View>
            ) : null}
          </View>
        );
      })}
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
    flexWrap: "wrap",
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
  panelHeaderRow: { flexDirection: "row", justifyContent: "space-between", alignItems: "center", gap: 12 },
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
    minWidth: 92,
    backgroundColor: "#10141C",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 8,
    paddingHorizontal: 10,
    paddingVertical: 7,
    color: "#F8FAFC",
    fontSize: 14,
  },
  inlineImageRow: { flexDirection: "row", alignItems: "center", gap: 8 },
  smallThumb: {
    width: 44,
    height: 44,
    borderRadius: 6,
    backgroundColor: "#FFFFFF",
    borderWidth: 1,
    borderColor: "#334155",
  },
  catalogCard: {
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    paddingVertical: 10,
    borderBottomWidth: 1,
    borderBottomColor: "#1E2534",
  },
  catalogImg: {
    width: 64,
    height: 64,
    borderRadius: 10,
    backgroundColor: "#FFFFFF",
    borderWidth: 2,
    borderColor: "#38BDF8",
  },
  catalogImgEmpty: { backgroundColor: "#10141C", borderColor: "#334155", alignItems: "center", justifyContent: "center" },
  catalogActions: { flexDirection: "row", alignItems: "center", gap: 8 },
  tierCard: {
    backgroundColor: "#10141C",
    borderColor: "#252D3D",
    borderWidth: 1,
    borderRadius: 10,
    padding: 12,
    gap: 10,
  },
  tierTopRow: { flexDirection: "row", alignItems: "center", gap: 10, flexWrap: "wrap" },
  tierBadge: {
    backgroundColor: "#0284C7",
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 6,
  },
  tierBadgeText: { color: "#FFFFFF", fontSize: 11, fontWeight: "800" },
  tierRangeText: { color: "#38BDF8", fontSize: 15, fontWeight: "800" },
  tierPrizeRow: { flexDirection: "row", alignItems: "center", gap: 10 },
  assetGrid: { flexDirection: "row", flexWrap: "wrap", gap: 10 },
  assetTile: {
    width: 104,
    padding: 8,
    borderRadius: 10,
    backgroundColor: "#10141C",
    borderWidth: 1,
    borderColor: "#2A3242",
    alignItems: "center",
    gap: 6,
  },
  assetTileSelected: { borderColor: "#38BDF8", backgroundColor: "#17253B" },
  assetTileImg: { width: 76, height: 76, borderRadius: 6, backgroundColor: "#FFFFFF" },
  assetTileLabel: { color: "#CBD5E1", fontSize: 11, fontWeight: "600" },
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
    maxWidth: 460,
    backgroundColor: "#161B24",
    borderColor: "#2A3242",
    borderWidth: 1,
    borderRadius: 14,
    padding: 20,
    gap: 14,
  },
});
