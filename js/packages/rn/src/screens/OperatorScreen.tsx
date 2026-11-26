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
  useWindowDimensions,
} from "react-native";
import type {
  ActionSpec,
  AssetMeta,
  CatalogEntry,
  CatalogSpec,
  DeviceRecord,
  FormField,
  RuleEntry,
  RulesetSpec,
  SettingSpec,
  SubmissionRecord,
} from "@corekiosk/client";
import { DynamicForm } from "../components/DynamicForm.js";
import { useKiosk, useOperator } from "../hooks.js";
import { OpColors, OpFonts, OpMetrics } from "../operatorTheme.js";

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

function formatApplyNote(spec: SettingSpec): string {
  const parts: string[] = [];
  if (spec.help) parts.push(spec.help);
  if (spec.apply === "next_round") parts.push("Applies next round");
  else if (spec.apply === "restart") parts.push("Requires restart");
  if (spec.scope === "runtime") parts.push("Session only");
  return parts.join(" · ");
}

function formatSubmissionSummary(payload: Record<string, unknown>, fields?: FormField[]): string {
  if (fields && fields.length > 0) {
    const parts: string[] = [];
    for (const f of fields) {
      if (f.key === "name") continue;
      const v = payload[f.key];
      if (v === undefined || v === null || String(v).trim() === "") continue;
      parts.push(f.prefix ? `${f.prefix}${String(v)}` : String(v));
    }
    if (parts.length > 0) return parts.join("  ·  ");
  }
  const entries = Object.entries(payload).filter(([k, v]) => k !== "name" && v !== undefined && v !== null && String(v) !== "");
  return entries.map(([, v]) => String(v)).join("  ·  ");
}

export function OperatorScreen({ extraControls }: OperatorScreenProps) {
  const { state, serverUrl, clientDeviceId, closeOperator, openConnect, unpair } = useKiosk();
  const op = useOperator();
  const { width } = useWindowDimensions();
  const useSideRail = width >= 880;

  const hasCatalogsOrRules =
    (state.schema?.catalogs?.length ?? 0) > 0 || (state.schema?.rulesets?.length ?? 0) > 0;
  const [tab, setTab] = useState<OpTab>(hasCatalogsOrRules ? "prizes" : "controls");
  const [activeSettingGroup, setActiveSettingGroup] = useState<string>("All");
  const [toast, setToast] = useState<string | null>(null);

  // Modals
  const [paramAction, setParamAction] = useState<ActionSpec | null>(null);
  const [paramValues, setParamValues] = useState<Record<string, unknown>>({});
  const [confirmDialog, setConfirmDialog] = useState<{
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    onConfirm: () => void;
  } | null>(null);

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

  // History & System
  const [historySearch, setHistorySearch] = useState("");
  const [submissions, setSubmissions] = useState<SubmissionRecord[]>([]);
  const [submissionsTotal, setSubmissionsTotal] = useState(0);
  const [devices, setDevices] = useState<DeviceRecord[]>([]);

  const showToast = useCallback((msg: string) => {
    setToast(msg);
    setTimeout(() => setToast((cur) => (cur === msg ? null : cur)), 3000);
  }, []);

  const loadHistory = useCallback(async () => {
    try {
      const res = await op.fetchSubmissions(historySearch, 100, 0);
      setSubmissions(res.items);
      setSubmissionsTotal(res.total);
    } catch (err) {
      showToast(err instanceof Error ? err.message : "Failed to load submissions");
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
      showToast("Select from server library below, or upload from Companion Web / Kiosk PC Launcher.");
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

  const settingGroupNames = useMemo(() => ["All", ...groupedSettings.map((g) => g.group)], [groupedSettings]);

  const filteredGroups = useMemo(() => {
    if (activeSettingGroup === "All" || !settingGroupNames.includes(activeSettingGroup)) {
      return groupedSettings;
    }
    return groupedSettings.filter((g) => g.group === activeSettingGroup);
  }, [activeSettingGroup, groupedSettings, settingGroupNames]);

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
      showToast(res.message || `${act.label} completed`);
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

  const confirmDeleteBoardRow = (boardId: string, entryId: string, rank: number, name: string) => {
    setConfirmDialog({
      title: "Remove Leaderboard Entry",
      message: `Remove #${rank} (${name}) from the public board? Player submission history is preserved.`,
      confirmLabel: "Remove",
      danger: true,
      onConfirm: async () => {
        try {
          await op.deleteBoardRow(boardId, entryId);
          showToast("Entry removed from board");
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
        showToast(`Saved ${catalogEntryTitle(spec, entry)}`);
      } else {
        await op.addCatalogEntry(spec.id, {
          id: entryDraftId.trim() || undefined,
          enabled: true,
          fields: entryDraftFields,
        });
        showToast(`Added item to ${spec.title}`);
      }
      setEditingCatalog(null);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const actions = state.schema?.actions ?? [];
  const catalogSpecs = state.schema?.catalogs ?? [];
  const rulesetSpecs = state.schema?.rulesets ?? [];
  const formFields = state.schema?.forms?.[0]?.fields;
  const boards = Object.values(state.boards);
  const wifi = state.snapshot?.wifi;
  const displayOnline = Boolean(state.snapshot?.display_online);
  const currentPhase = state.snapshot?.phase ?? "idle";

  const visibleTabs: Array<{ id: OpTab; label: string }> = [
    ...(hasCatalogsOrRules ? [{ id: "prizes" as OpTab, label: "Prizes & Tiers" }] : []),
    { id: "controls", label: "Settings" },
    { id: "boards", label: "Leaderboard" },
    { id: "history", label: "Players" },
    { id: "system", label: "System" },
  ];

  return (
    <View style={styles.root}>
      {/* Top Console Bar */}
      <View style={styles.topBar}>
        <View style={styles.topBarLeft}>
          <Text style={styles.consoleTitle}>Operator Console</Text>
          <View style={styles.statusInline}>
            <View style={[styles.statusDot, { backgroundColor: displayOnline ? OpColors.ok : OpColors.danger }]} />
            <Text style={styles.statusLabel}>
              {displayOnline ? `Display Online · ${currentPhase}` : "Display Offline"}
            </Text>
          </View>
        </View>

        <Pressable
          style={({ pressed }) => [styles.exitBtn, pressed && { opacity: 0.86 }]}
          onPress={closeOperator}
        >
          <Text style={styles.exitBtnText}>Done</Text>
        </Pressable>
      </View>

      {/* Responsive Workspace: Left Navigation Rail on Tablet Landscape / Web, Top Bar on Portrait */}
      <View style={[styles.workspace, useSideRail ? styles.workspaceRow : styles.workspaceCol]}>
        {useSideRail ? (
          <View style={styles.sideRail}>
            {visibleTabs.map((t) => {
              const active = tab === t.id;
              return (
                <Pressable
                  key={t.id}
                  onPress={() => setTab(t.id)}
                  style={({ pressed }) => [
                    styles.railItem,
                    active && styles.railItemActive,
                    !active && pressed && styles.railItemPressed,
                  ]}
                >
                  <Text style={[styles.railText, active && styles.railTextActive]}>{t.label}</Text>
                </Pressable>
              );
            })}
            <View style={{ flex: 1 }} />
            <View style={styles.railFooter}>
              <Text style={styles.railMeta}>Signed in as {op.username ?? "operator"}</Text>
              <Text style={styles.railMetaMono} numberOfLines={1}>
                {serverUrl.replace(/^https?:\/\//, "")}
              </Text>
            </View>
          </View>
        ) : (
          <View style={styles.topTabsWrap}>
            <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.topTabs}>
              {visibleTabs.map((t) => {
                const active = tab === t.id;
                return (
                  <Pressable
                    key={t.id}
                    onPress={() => setTab(t.id)}
                    style={[styles.topTabBtn, active && styles.topTabBtnActive]}
                  >
                    <Text style={[styles.topTabText, active && styles.topTabTextActive]}>{t.label}</Text>
                  </Pressable>
                );
              })}
            </ScrollView>
          </View>
        )}

        {/* Main Content Sheet */}
        <ScrollView style={styles.mainScroll} contentContainerStyle={styles.mainContent}>
          <View style={styles.contentColumn}>
            {/* TAB: PRIZES & TIERS */}
            {tab === "prizes" ? (
              <>
                {rulesetSpecs.map((rSpec) => (
                  <RulesetEditorSection
                    key={rSpec.id}
                    spec={rSpec}
                    rules={state.rulesets[rSpec.id]?.rules ?? rSpec.defaults ?? []}
                    catalogSpec={catalogSpecs.find((c) => c.id === rSpec.catalog_id)}
                    catalogEntries={rSpec.catalog_id ? state.catalogs[rSpec.catalog_id]?.entries ?? [] : []}
                    assetUrl={op.assetUrl}
                    onSave={async (nextRules) => {
                      try {
                        await op.saveRuleset(rSpec.id, nextRules);
                        showToast(`Updated ${rSpec.title}`);
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
                    <View key={cSpec.id} style={styles.section}>
                      <View style={styles.sectionHeader}>
                        <View style={{ flex: 1, gap: 4 }}>
                          <Text style={styles.sectionTitle}>{cSpec.title}</Text>
                          {cSpec.help ? <Text style={styles.sectionSub}>{cSpec.help}</Text> : null}
                        </View>
                        {cSpec.allow_add_delete ? (
                          <Pressable
                            style={({ pressed }) => [styles.accentBtn, pressed && { opacity: 0.88 }]}
                            onPress={() => openCatalogEntryModal(cSpec, null)}
                          >
                            <Text style={styles.accentBtnText}>+ Add Prize</Text>
                          </Pressable>
                        ) : null}
                      </View>

                      <View style={styles.sheet}>
                        {entries.map((entry, idx) => {
                          const imgId = catalogEntryImageId(cSpec, entry);
                          const title = catalogEntryTitle(cSpec, entry);
                          return (
                            <View
                              key={entry.id}
                              style={[styles.prizeRow, idx > 0 && styles.rowDivider, !entry.enabled && styles.prizeRowMuted]}
                            >
                              <Pressable
                                style={styles.prizeThumbWrap}
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
                                  <Image source={{ uri: op.assetUrl(imgId) }} style={styles.prizeThumb} resizeMode="contain" />
                                ) : (
                                  <View style={[styles.prizeThumb, styles.prizeThumbEmpty]}>
                                    <Text style={styles.metaText}>Add PNG</Text>
                                  </View>
                                )}
                              </Pressable>

                              <View style={{ flex: 1, gap: 4 }}>
                                <Text style={styles.prizeTitle}>{title}</Text>
                                <Text style={styles.metaText}>
                                  {entry.enabled ? "In stock & active" : "Paused (out of stock — tiers skip this item)"}
                                </Text>
                              </View>

                              <View style={styles.prizeControls}>
                                <Switch
                                  value={entry.enabled}
                                  onValueChange={(v) =>
                                    void op
                                      .patchCatalogEntry(cSpec.id, entry.id, { enabled: v })
                                      .catch((err) => showToast(err instanceof Error ? err.message : String(err)))
                                  }
                                  trackColor={{ false: OpColors.lineStrong, true: OpColors.ok }}
                                />
                                {imgKey ? (
                                  <Pressable
                                    style={({ pressed }) => [styles.neutralBtn, pressed && styles.neutralBtnPressed]}
                                    onPress={() =>
                                      openAssetPicker(imgId, (newAssetId) => {
                                        void op
                                          .patchCatalogEntry(cSpec.id, entry.id, { fields: { [imgKey]: newAssetId } })
                                          .then(() => showToast(`Updated image for ${title}`))
                                          .catch((err) => showToast(err instanceof Error ? err.message : String(err)));
                                      })
                                    }
                                  >
                                    <Text style={styles.neutralBtnText}>Image</Text>
                                  </Pressable>
                                ) : null}
                                <Pressable
                                  style={({ pressed }) => [styles.neutralBtn, pressed && styles.neutralBtnPressed]}
                                  onPress={() => openCatalogEntryModal(cSpec, entry)}
                                >
                                  <Text style={styles.neutralBtnText}>Rename</Text>
                                </Pressable>
                                {cSpec.allow_add_delete ? (
                                  <Pressable
                                    style={styles.dangerGhostBtn}
                                    onPress={() =>
                                      setConfirmDialog({
                                        title: `Delete ${title}?`,
                                        message: `Remove "${title}" from ${cSpec.title}?`,
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
                                    <Text style={styles.dangerGhostText}>Delete</Text>
                                  </Pressable>
                                ) : null}
                              </View>
                            </View>
                          );
                        })}
                      </View>
                    </View>
                  );
                })}
              </>
            ) : null}

            {/* TAB: SETTINGS & ACTIONS */}
            {tab === "controls" ? (
              <>
                {extraControls}

                {settingGroupNames.length > 2 ? (
                  <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.filterStrip}>
                    {settingGroupNames.map((name) => {
                      const active = activeSettingGroup === name;
                      return (
                        <Pressable
                          key={name}
                          onPress={() => setActiveSettingGroup(name)}
                          style={[styles.filterChip, active && styles.filterChipActive]}
                        >
                          <Text style={[styles.filterChipText, active && styles.filterChipTextActive]}>{name}</Text>
                        </Pressable>
                      );
                    })}
                  </ScrollView>
                ) : null}

                {filteredGroups.map(({ group, items }) => (
                  <View key={group} style={styles.section}>
                    <Text style={styles.sectionTitle}>{group}</Text>
                    <View style={styles.sheet}>
                      {items.map((spec, idx) => {
                        const val = spec.scope === "runtime" ? state.runtimeValues[spec.key] : state.values[spec.key];
                        const note = formatApplyNote(spec);
                        return (
                          <View key={spec.key} style={[styles.settingRow, idx > 0 && styles.rowDivider]}>
                            <View style={styles.settingMeta}>
                              <Text style={styles.settingLabel}>{spec.label}</Text>
                              {note ? <Text style={styles.settingNote}>{note}</Text> : null}
                            </View>

                            {spec.type === "bool" ? (
                              <Switch
                                value={Boolean(val)}
                                onValueChange={(v) => void onPatch(spec.key, v)}
                                trackColor={{ false: OpColors.lineStrong, true: OpColors.selectedBg }}
                              />
                            ) : spec.type === "image" ? (
                              <View style={styles.inlineImageRow}>
                                {typeof val === "string" && val ? (
                                  <Image source={{ uri: op.assetUrl(val) }} style={styles.smallThumb} resizeMode="contain" />
                                ) : null}
                                <Pressable
                                  style={({ pressed }) => [styles.neutralBtn, pressed && styles.neutralBtnPressed]}
                                  onPress={() =>
                                    openAssetPicker(typeof val === "string" ? val : "", (id) => void onPatch(spec.key, id))
                                  }
                                >
                                  <Text style={styles.neutralBtnText}>{val ? "Replace…" : "Upload…"}</Text>
                                </Pressable>
                              </View>
                            ) : spec.type === "enum" && spec.options?.length ? (
                              <View style={styles.segmentedTrack}>
                                {spec.options.map((o) => {
                                  const active = val === o.value;
                                  return (
                                    <Pressable
                                      key={o.value}
                                      style={[styles.segmentOption, active && styles.segmentOptionActive]}
                                      onPress={() => void onPatch(spec.key, o.value)}
                                    >
                                      <Text style={[styles.segmentOptionText, active && styles.segmentOptionTextActive]}>
                                        {o.label}
                                      </Text>
                                    </Pressable>
                                  );
                                })}
                              </View>
                            ) : spec.type === "int" || spec.type === "number" ? (
                              <NumericStepperControl
                                value={typeof val === "number" ? val : Number(val ?? 0)}
                                integer={spec.type === "int"}
                                min={spec.min}
                                max={spec.max}
                                step={spec.step}
                                unit={spec.unit}
                                onCommit={(next) => void onPatch(spec.key, next)}
                              />
                            ) : (
                              <CommitTextInput
                                initialValue={val !== undefined && val !== null ? String(val) : ""}
                                placeholder={spec.placeholder}
                                onCommit={(txt) => void onPatch(spec.key, txt)}
                              />
                            )}
                          </View>
                        );
                      })}
                    </View>
                  </View>
                ))}

                {actions.length > 0 ? (
                  <View style={styles.section}>
                    <Text style={styles.sectionTitle}>Quick Commands</Text>
                    <View style={styles.actionRowWrap}>
                      {actions.map((act) => (
                        <Pressable
                          key={act.id}
                          onPress={() => onPressAction(act)}
                          style={({ pressed }) => [
                            styles.commandBtn,
                            act.style === "danger" && styles.commandBtnDanger,
                            act.style === "primary" && styles.commandBtnPrimary,
                            pressed && { opacity: 0.85 },
                          ]}
                        >
                          <Text
                            style={[
                              styles.commandBtnText,
                              act.style === "danger" && styles.commandBtnDangerText,
                              act.style === "primary" && styles.commandBtnPrimaryText,
                            ]}
                          >
                            {act.label}
                          </Text>
                        </Pressable>
                      ))}
                    </View>
                  </View>
                ) : null}
              </>
            ) : null}

            {/* TAB: LEADERBOARD */}
            {tab === "boards" ? (
              boards.length === 0 ? (
                <Text style={styles.emptyState}>No leaderboards configured.</Text>
              ) : (
                boards.map((board) => (
                  <View key={board.board_id} style={styles.section}>
                    <View style={styles.sectionHeader}>
                      <Text style={styles.sectionTitle}>
                        {board.title} ({board.rows.length})
                      </Text>
                    </View>
                    <View style={styles.sheet}>
                      {board.rows.length === 0 ? (
                        <View style={styles.emptySheet}>
                          <Text style={styles.emptyState}>No active scores on this board yet.</Text>
                        </View>
                      ) : (
                        board.rows.map((r, idx) => {
                          const playerName = String(r.display.name ?? r.entry_id);
                          return (
                            <View key={r.entry_id} style={[styles.boardRow, idx > 0 && styles.rowDivider]}>
                              <Text style={styles.boardRank}>#{r.rank}</Text>
                              <View style={{ flex: 1, gap: 2 }}>
                                <Text style={styles.boardName}>{playerName}</Text>
                                {r.display.reward ? (
                                  <Text style={styles.metaText}>Prize: {String(r.display.reward)}</Text>
                                ) : null}
                              </View>
                              <Text style={styles.boardScore}>{r.sort_value.toLocaleString()}</Text>
                              <Pressable
                                style={styles.dangerGhostBtn}
                                onPress={() => confirmDeleteBoardRow(board.board_id, r.entry_id, r.rank, playerName)}
                              >
                                <Text style={styles.dangerGhostText}>Remove</Text>
                              </Pressable>
                            </View>
                          );
                        })
                      )}
                    </View>
                  </View>
                ))
              )
            ) : null}

            {/* TAB: PLAYERS / SUBMISSIONS HISTORY */}
            {tab === "history" ? (
              <View style={styles.section}>
                <View style={styles.sectionHeader}>
                  <Text style={styles.sectionTitle}>Player Registrations ({submissionsTotal})</Text>
                </View>

                <View style={styles.searchBar}>
                  <TextInput
                    value={historySearch}
                    onChangeText={setHistorySearch}
                    placeholder="Search player name, handle, or email…"
                    placeholderTextColor={OpColors.inkMuted}
                    onSubmitEditing={() => void loadHistory()}
                    style={styles.searchInput}
                  />
                  <Pressable
                    style={({ pressed }) => [styles.selectedBtn, pressed && { opacity: 0.88 }]}
                    onPress={() => void loadHistory()}
                  >
                    <Text style={styles.selectedBtnText}>Search</Text>
                  </Pressable>
                </View>

                <View style={styles.sheet}>
                  {submissions.length === 0 ? (
                    <View style={styles.emptySheet}>
                      <Text style={styles.emptyState}>No registrations recorded yet.</Text>
                    </View>
                  ) : (
                    submissions.map((sub, idx) => {
                      const playerName = String(sub.payload.name ?? sub.id);
                      const details = formatSubmissionSummary(sub.payload, formFields);
                      const scoreVal = sub.outcome?.score;
                      const wonPrize = sub.outcome?.reward_title ? String(sub.outcome.reward_title) : "";
                      return (
                        <View key={sub.id} style={[styles.historyRow, idx > 0 && styles.rowDivider]}>
                          <View style={{ flex: 1, gap: 4 }}>
                            <Text style={styles.boardName}>{playerName}</Text>
                            {details ? <Text style={styles.settingNote}>{details}</Text> : null}
                            <Text style={styles.metaText}>
                              {new Date(sub.created_at_ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
                              {"  ·  "}
                              {sub.status}
                            </Text>
                          </View>

                          <View style={styles.historyOutcomeCol}>
                            {scoreVal !== undefined ? (
                              <Text style={styles.historyScore}>{Number(scoreVal).toLocaleString()} pts</Text>
                            ) : null}
                            {wonPrize ? (
                              <View style={styles.prizeTag}>
                                <Text style={styles.prizeTagText}>{wonPrize}</Text>
                              </View>
                            ) : null}
                          </View>
                        </View>
                      );
                    })
                  )}
                </View>
              </View>
            ) : null}

            {/* TAB: SYSTEM */}
            {tab === "system" ? (
              <>
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Kiosk Connection</Text>
                  <View style={styles.sheet}>
                    <View style={styles.kvRow}>
                      <Text style={styles.kvKey}>Server URL</Text>
                      <Text style={styles.kvValMono}>{serverUrl}</Text>
                    </View>
                    <View style={[styles.kvRow, styles.rowDivider]}>
                      <Text style={styles.kvKey}>Kiosk Wi-Fi</Text>
                      <Text style={styles.kvVal}>
                        {wifi?.connected && wifi.ssid ? `${wifi.ssid} (${wifi.signal_pct ?? 0}%)` : "Ethernet / Offline"}
                      </Text>
                    </View>
                    <View style={[styles.kvRow, styles.rowDivider]}>
                      <Text style={styles.kvKey}>This Companion Key</Text>
                      <Text style={styles.kvValMono}>{clientDeviceId}</Text>
                    </View>
                    <View style={[styles.kvRow, styles.rowDivider]}>
                      <Pressable
                        style={({ pressed }) => [styles.neutralBtn, pressed && styles.neutralBtnPressed]}
                        onPress={openConnect}
                      >
                        <Text style={styles.neutralBtnText}>Change Kiosk IP…</Text>
                      </Pressable>
                      <Pressable
                        style={styles.dangerGhostBtn}
                        onPress={() =>
                          setConfirmDialog({
                            title: "Unpair This Device?",
                            message: "You will need the 6-digit pairing code on the kiosk to reconnect.",
                            confirmLabel: "Unpair",
                            danger: true,
                            onConfirm: () => void unpair(),
                          })
                        }
                      >
                        <Text style={styles.dangerGhostText}>Unpair Device</Text>
                      </Pressable>
                    </View>
                  </View>
                </View>

                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Paired Hardware ({devices.length})</Text>
                  <View style={styles.sheet}>
                    {devices.map((d, idx) => (
                      <View key={d.device_id} style={[styles.historyRow, idx > 0 && styles.rowDivider]}>
                        <View style={{ flex: 1, gap: 3 }}>
                          <Text style={styles.boardName}>{d.device_name}</Text>
                          <Text style={styles.metaText}>
                            {d.role}  ·  {d.model || d.device_type}  ·  {d.last_ip || "local"}
                          </Text>
                        </View>
                        <Text style={styles.kvValMono}>{d.device_id.slice(0, 10)}</Text>
                      </View>
                    ))}
                  </View>
                </View>
              </>
            ) : null}
          </View>
        </ScrollView>
      </View>

      {toast ? (
        <View style={styles.toast}>
          <Text style={styles.toastText}>{toast}</Text>
        </View>
      ) : null}

      {/* Catalog Entry Add/Edit Modal */}
      <Modal visible={Boolean(editingCatalog)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalSheet}>
            <Text style={styles.modalTitle}>
              {editingCatalog?.entry ? `Edit ${catalogEntryTitle(editingCatalog.spec, editingCatalog.entry)}` : `Add to ${editingCatalog?.spec.title ?? ""}`}
            </Text>
            {editingCatalog && !editingCatalog.entry ? (
              <View style={{ gap: 8 }}>
                <Text style={styles.settingLabel}>Identifier (optional)</Text>
                <TextInput
                  value={entryDraftId}
                  onChangeText={setEntryDraftId}
                  placeholder="e.g. hoodie"
                  placeholderTextColor={OpColors.inkMuted}
                  autoCapitalize="none"
                  style={styles.modalInput}
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
            <View style={styles.modalActions}>
              <Pressable style={styles.neutralBtn} onPress={() => setEditingCatalog(null)}>
                <Text style={styles.neutralBtnText}>Cancel</Text>
              </Pressable>
              <Pressable style={styles.accentBtn} onPress={() => void saveCatalogEntryModal()}>
                <Text style={styles.accentBtnText}>Save</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>

      {/* Asset Upload / Media Library Modal */}
      <Modal visible={Boolean(assetPicker)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={[styles.modalSheet, { maxWidth: 560 }]}>
            <Text style={styles.modalTitle}>Prize Image</Text>
            {Platform.OS === "web" ? (
              <Pressable
                disabled={uploadingAsset}
                style={({ pressed }) => [styles.accentBtn, { alignSelf: "flex-start" }, pressed && { opacity: 0.88 }]}
                onPress={() =>
                  triggerWebFileUpload((newId) => {
                    const cb = assetPicker?.onSelect;
                    setAssetPicker(null);
                    cb?.(newId);
                  })
                }
              >
                <Text style={styles.accentBtnText}>
                  {uploadingAsset ? "Uploading…" : "Upload PNG / JPG / WebP from Computer…"}
                </Text>
              </Pressable>
            ) : (
              <Text style={styles.settingNote}>
                Upload new artwork from Companion Web or the Kiosk PC Launcher, then tap an image below to select it.
              </Text>
            )}

            <Text style={styles.settingLabel}>Stored Images ({recentAssets.length})</Text>
            <ScrollView style={{ maxHeight: 300 }} contentContainerStyle={styles.assetGrid}>
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

            <View style={styles.modalActions}>
              <Pressable style={styles.neutralBtn} onPress={() => setAssetPicker(null)}>
                <Text style={styles.neutralBtnText}>Close</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>

      {/* Action Parameters Modal */}
      <Modal visible={Boolean(paramAction)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalSheet}>
            <Text style={styles.modalTitle}>{paramAction?.label}</Text>
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
            <View style={styles.modalActions}>
              <Pressable style={styles.neutralBtn} onPress={() => setParamAction(null)}>
                <Text style={styles.neutralBtnText}>Cancel</Text>
              </Pressable>
              <Pressable
                style={styles.accentBtn}
                onPress={() => {
                  const act = paramAction;
                  setParamAction(null);
                  if (act) void runAction(act, paramValues);
                }}
              >
                <Text style={styles.accentBtnText}>Run</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>

      {/* Confirmation Modal */}
      <Modal visible={Boolean(confirmDialog)} transparent animationType="fade">
        <View style={styles.modalBackdrop}>
          <View style={styles.modalSheet}>
            <Text style={styles.modalTitle}>{confirmDialog?.title}</Text>
            <Text style={styles.modalBody}>{confirmDialog?.message}</Text>
            <View style={styles.modalActions}>
              <Pressable style={styles.neutralBtn} onPress={() => setConfirmDialog(null)}>
                <Text style={styles.neutralBtnText}>Cancel</Text>
              </Pressable>
              <Pressable
                style={confirmDialog?.danger ? styles.dangerSolidBtn : styles.accentBtn}
                onPress={() => {
                  const fn = confirmDialog?.onConfirm;
                  setConfirmDialog(null);
                  fn?.();
                }}
              >
                <Text style={styles.accentBtnText}>{confirmDialog?.confirmLabel ?? "Confirm"}</Text>
              </Pressable>
            </View>
          </View>
        </View>
      </Modal>
    </View>
  );
}

function RulesetEditorSection({
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
    <View style={styles.section}>
      <View style={styles.sectionHeader}>
        <View style={{ flex: 1, gap: 4 }}>
          <Text style={styles.sectionTitle}>{spec.title}</Text>
          <Text style={styles.sectionSub}>
            {spec.help || "Set minimum score per tier. Score ranges adjust automatically without gaps."}
          </Text>
        </View>
        {spec.allow_add_delete ? (
          <Pressable style={({ pressed }) => [styles.neutralBtn, pressed && styles.neutralBtnPressed]} onPress={addTier}>
            <Text style={styles.neutralBtnText}>+ Add Tier</Text>
          </Pressable>
        ) : null}
      </View>

      <View style={styles.sheet}>
        {sortedRules.map((rule, idx) => {
          const upper = idx > 0 ? Math.max(rule.min_score, sortedRules[idx - 1].min_score - 1) : null;
          const rangeLabel = upper === null ? `${rule.min_score}+ pts` : `${rule.min_score} – ${upper} pts`;
          const selectedEntry = catalogEntries.find((e) => e.id === rule.entry_id);
          const imgId = selectedEntry && catalogSpec ? catalogEntryImageId(catalogSpec, selectedEntry) : "";

          return (
            <View key={rule.id} style={[styles.tierRow, idx > 0 && styles.rowDivider]}>
              <View style={styles.tierHeaderLine}>
                <View style={styles.tierTitleBlock}>
                  <Text style={styles.tierIndexLabel}>Tier {idx + 1}</Text>
                  <Text style={styles.tierRangeBig}>{rangeLabel}</Text>
                </View>

                <View style={styles.tierThresholdControls}>
                  <Text style={styles.metaText}>Min score</Text>
                  <NumericStepperControl
                    value={rule.min_score}
                    integer
                    min={0}
                    step={10}
                    onCommit={(n) => updateRule(rule.id, { min_score: Math.max(0, n) })}
                  />
                  {spec.allow_add_delete && sortedRules.length > 1 ? (
                    <Pressable style={styles.dangerGhostBtn} onPress={() => removeTier(rule.id)}>
                      <Text style={styles.dangerGhostText}>Remove</Text>
                    </Pressable>
                  ) : null}
                </View>
              </View>

              {catalogSpec && catalogEntries.length > 0 ? (
                <View style={styles.tierPrizePickerRow}>
                  {imgId ? (
                    <Image source={{ uri: assetUrl(imgId) }} style={styles.tierPrizePreview} resizeMode="contain" />
                  ) : null}
                  <View style={styles.tierPrizeChoices}>
                    {catalogEntries.map((ce) => {
                      const active = rule.entry_id === ce.id;
                      const label = catalogEntryTitle(catalogSpec, ce) + (ce.enabled ? "" : " (paused)");
                      return (
                        <Pressable
                          key={ce.id}
                          style={[styles.choiceBtn, active && styles.choiceBtnActive]}
                          onPress={() => updateRule(rule.id, { entry_id: ce.id })}
                        >
                          <Text style={[styles.choiceBtnText, active && styles.choiceBtnTextActive]}>{label}</Text>
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
    </View>
  );
}

function NumericStepperControl({
  value,
  integer,
  min,
  max,
  step,
  unit,
  onCommit,
}: {
  value: number;
  integer: boolean;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  onCommit: (next: number) => void;
}) {
  const effectiveStep = step && step > 0 ? step : integer ? 1 : 0.05;
  const decimals = integer ? 0 : effectiveStep < 0.1 ? 2 : 1;
  const formatted = Number.isFinite(value) ? (integer ? String(Math.round(value)) : Number(value.toFixed(decimals)).toString()) : "0";

  const [text, setText] = useState(formatted);
  useEffect(() => {
    setText(formatted);
  }, [formatted]);

  const clampAndCommit = (raw: number) => {
    if (Number.isNaN(raw)) return;
    let next = integer ? Math.round(raw) : Number(raw.toFixed(decimals));
    if (min !== undefined && next < min) next = min;
    if (max !== undefined && next > max) next = max;
    onCommit(next);
  };

  return (
    <View style={styles.stepperWrap}>
      <Pressable
        style={({ pressed }) => [styles.stepBtn, pressed && styles.neutralBtnPressed]}
        onPress={() => clampAndCommit(value - effectiveStep)}
      >
        <Text style={styles.stepBtnLabel}>−</Text>
      </Pressable>

      <View style={styles.stepInputBox}>
        <TextInput
          value={text}
          onChangeText={setText}
          onBlur={() => {
            if (text.trim() !== formatted) clampAndCommit(parseFloat(text.trim()));
          }}
          onSubmitEditing={() => clampAndCommit(parseFloat(text.trim()))}
          keyboardType="numeric"
          style={styles.stepInput}
        />
        {unit ? <Text style={styles.unitLabel}>{unit}</Text> : null}
      </View>

      <Pressable
        style={({ pressed }) => [styles.stepBtn, pressed && styles.neutralBtnPressed]}
        onPress={() => clampAndCommit(value + effectiveStep)}
      >
        <Text style={styles.stepBtnLabel}>+</Text>
      </Pressable>
    </View>
  );
}

function CommitTextInput({
  initialValue,
  placeholder,
  onCommit,
}: {
  initialValue: string;
  placeholder?: string;
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
      placeholder={placeholder}
      placeholderTextColor={OpColors.inkMuted}
      onBlur={() => {
        if (text.trim() !== initialValue) onCommit(text.trim());
      }}
      onSubmitEditing={() => onCommit(text.trim())}
      style={styles.textSettingInput}
    />
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: OpColors.ground },

  // Top Header Bar
  topBar: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    paddingHorizontal: 24,
    paddingVertical: 16,
    backgroundColor: OpColors.surface,
    borderBottomWidth: 1,
    borderBottomColor: OpColors.line,
  },
  topBarLeft: { gap: 4 },
  consoleTitle: {
    color: OpColors.ink,
    fontSize: 22,
    fontWeight: "700",
    fontFamily: OpFonts.sans,
    letterSpacing: -0.3,
  },
  statusInline: { flexDirection: "row", alignItems: "center", gap: 8 },
  statusDot: { width: 9, height: 9, borderRadius: 5 },
  statusLabel: { color: OpColors.inkSecondary, fontSize: 14, fontWeight: "500", fontFamily: OpFonts.sans },

  exitBtn: {
    minHeight: 48,
    backgroundColor: OpColors.selectedBg,
    paddingHorizontal: 24,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  exitBtnText: { color: OpColors.selectedFg, fontWeight: "700", fontSize: 16, fontFamily: OpFonts.sans },

  // Workspace Layout
  workspace: { flex: 1 },
  workspaceRow: { flexDirection: "row" },
  workspaceCol: { flexDirection: "column" },

  // Wide Left Rail
  sideRail: {
    width: 228,
    backgroundColor: OpColors.surface,
    borderRightWidth: 1,
    borderRightColor: OpColors.line,
    padding: 16,
    gap: 6,
  },
  railItem: {
    minHeight: 48,
    paddingHorizontal: 16,
    borderRadius: OpMetrics.radiusMd,
    justifyContent: "center",
  },
  railItemActive: { backgroundColor: OpColors.selectedBg },
  railItemPressed: { backgroundColor: OpColors.recessed },
  railText: { color: OpColors.inkSecondary, fontSize: 16, fontWeight: "600", fontFamily: OpFonts.sans },
  railTextActive: { color: OpColors.selectedFg, fontWeight: "700" },
  railFooter: {
    borderTopWidth: 1,
    borderTopColor: OpColors.line,
    paddingTop: 14,
    paddingHorizontal: 8,
    gap: 4,
  },
  railMeta: { color: OpColors.inkSecondary, fontSize: 13, fontFamily: OpFonts.sans },
  railMetaMono: { color: OpColors.inkMuted, fontSize: 12, fontFamily: OpFonts.mono },

  // Portrait Top Tabs
  topTabsWrap: {
    backgroundColor: OpColors.surface,
    borderBottomWidth: 1,
    borderBottomColor: OpColors.line,
  },
  topTabs: { paddingHorizontal: 16, paddingVertical: 10, gap: 8 },
  topTabBtn: {
    minHeight: 44,
    paddingHorizontal: 18,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.recessed,
    justifyContent: "center",
  },
  topTabBtnActive: { backgroundColor: OpColors.selectedBg },
  topTabText: { color: OpColors.inkSecondary, fontWeight: "600", fontSize: 15, fontFamily: OpFonts.sans },
  topTabTextActive: { color: OpColors.selectedFg, fontWeight: "700" },

  // Main Scroll & Constrained Column
  mainScroll: { flex: 1 },
  mainContent: { padding: 24, paddingBottom: 64, alignItems: "center" },
  contentColumn: { width: "100%", maxWidth: 880, gap: 28 },

  // Sections & Clean Flat Sheets (no nested box-inside-box clutter)
  section: { gap: 12 },
  sectionHeader: { flexDirection: "row", justifyContent: "space-between", alignItems: "flex-end", gap: 16 },
  sectionTitle: {
    color: OpColors.ink,
    fontSize: 20,
    fontWeight: "700",
    fontFamily: OpFonts.sans,
    letterSpacing: -0.2,
  },
  sectionSub: { color: OpColors.inkSecondary, fontSize: 15, lineHeight: 21, fontFamily: OpFonts.sans },

  sheet: {
    backgroundColor: OpColors.surface,
    borderColor: OpColors.line,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusLg,
    overflow: "hidden",
  },
  rowDivider: { borderTopWidth: 1, borderTopColor: OpColors.line },

  // Filter Chips for Settings
  filterStrip: { gap: 8, paddingBottom: 4 },
  filterChip: {
    minHeight: 42,
    paddingHorizontal: 16,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.recessed,
    justifyContent: "center",
  },
  filterChipActive: { backgroundColor: OpColors.selectedBg },
  filterChipText: { color: OpColors.ink, fontSize: 15, fontWeight: "600", fontFamily: OpFonts.sans },
  filterChipTextActive: { color: OpColors.selectedFg },

  // Setting Rows
  settingRow: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 16,
    paddingHorizontal: 20,
    paddingVertical: 16,
    minHeight: 68,
  },
  settingMeta: { flex: 1, minWidth: 200, gap: 3 },
  settingLabel: { color: OpColors.ink, fontSize: 17, fontWeight: "600", fontFamily: OpFonts.sans },
  settingNote: { color: OpColors.inkSecondary, fontSize: 14, fontFamily: OpFonts.sans },

  // Segmented Enum Track
  segmentedTrack: {
    flexDirection: "row",
    flexWrap: "wrap",
    backgroundColor: OpColors.recessed,
    padding: 4,
    borderRadius: OpMetrics.radiusMd,
    gap: 4,
  },
  segmentOption: {
    minHeight: 40,
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderRadius: OpMetrics.radiusSm,
    justifyContent: "center",
  },
  segmentOptionActive: {
    backgroundColor: OpColors.surface,
    shadowColor: "#000",
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.08,
    shadowRadius: 2,
    elevation: 1,
  },
  segmentOptionText: { color: OpColors.inkSecondary, fontSize: 15, fontWeight: "600", fontFamily: OpFonts.sans },
  segmentOptionTextActive: { color: OpColors.ink, fontWeight: "700" },

  // Tactile Numeric Stepper
  stepperWrap: { flexDirection: "row", alignItems: "center", gap: 6 },
  stepBtn: {
    width: 46,
    height: 46,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.recessed,
    alignItems: "center",
    justifyContent: "center",
  },
  stepBtnLabel: { color: OpColors.ink, fontSize: 22, fontWeight: "600", lineHeight: 24 },
  stepInputBox: {
    flexDirection: "row",
    alignItems: "center",
    minHeight: 46,
    minWidth: 96,
    backgroundColor: OpColors.ground,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 12,
    gap: 4,
  },
  stepInput: {
    minWidth: 52,
    textAlign: "right",
    color: OpColors.ink,
    fontSize: 17,
    fontWeight: "700",
    fontFamily: OpFonts.mono,
    paddingVertical: 8,
  },
  unitLabel: { color: OpColors.inkMuted, fontSize: 14, fontFamily: OpFonts.mono },

  textSettingInput: {
    minWidth: 200,
    minHeight: 46,
    backgroundColor: OpColors.ground,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 14,
    color: OpColors.ink,
    fontSize: 16,
    fontFamily: OpFonts.sans,
  },

  // Prizes & Score Tiers
  tierRow: { padding: 20, gap: 16 },
  tierHeaderLine: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 16,
  },
  tierTitleBlock: { gap: 2 },
  tierIndexLabel: { color: OpColors.inkSecondary, fontSize: 14, fontWeight: "600", fontFamily: OpFonts.sans },
  tierRangeBig: { color: OpColors.ink, fontSize: 22, fontWeight: "700", fontFamily: OpFonts.mono },
  tierThresholdControls: { flexDirection: "row", alignItems: "center", gap: 10 },
  tierPrizePickerRow: { flexDirection: "row", alignItems: "center", gap: 16, flexWrap: "wrap" },
  tierPrizePreview: {
    width: 60,
    height: 60,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.ground,
    borderWidth: 1,
    borderColor: OpColors.line,
  },
  tierPrizeChoices: { flexDirection: "row", flexWrap: "wrap", gap: 8, flex: 1 },
  choiceBtn: {
    minHeight: 44,
    paddingHorizontal: 16,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.recessed,
    justifyContent: "center",
  },
  choiceBtnActive: { backgroundColor: OpColors.selectedBg },
  choiceBtnText: { color: OpColors.ink, fontSize: 15, fontWeight: "600", fontFamily: OpFonts.sans },
  choiceBtnTextActive: { color: OpColors.selectedFg, fontWeight: "700" },

  prizeRow: {
    flexDirection: "row",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 16,
    padding: 20,
  },
  prizeRowMuted: { opacity: 0.65 },
  prizeThumbWrap: {},
  prizeThumb: {
    width: 80,
    height: 80,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.ground,
    borderWidth: 1,
    borderColor: OpColors.line,
  },
  prizeThumbEmpty: { alignItems: "center", justifyContent: "center" },
  prizeTitle: { color: OpColors.ink, fontSize: 19, fontWeight: "700", fontFamily: OpFonts.sans },
  prizeControls: { flexDirection: "row", alignItems: "center", gap: 10, flexWrap: "wrap" },

  inlineImageRow: { flexDirection: "row", alignItems: "center", gap: 12 },
  smallThumb: {
    width: 52,
    height: 52,
    borderRadius: OpMetrics.radiusSm,
    backgroundColor: OpColors.ground,
    borderWidth: 1,
    borderColor: OpColors.line,
  },

  // Buttons
  accentBtn: {
    minHeight: 46,
    backgroundColor: OpColors.accent,
    paddingHorizontal: 20,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  accentBtnText: { color: OpColors.inkInverse, fontWeight: "700", fontSize: 15, fontFamily: OpFonts.sans },

  selectedBtn: {
    minHeight: 48,
    backgroundColor: OpColors.selectedBg,
    paddingHorizontal: 22,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  selectedBtnText: { color: OpColors.selectedFg, fontWeight: "700", fontSize: 16, fontFamily: OpFonts.sans },

  neutralBtn: {
    minHeight: 44,
    backgroundColor: OpColors.recessed,
    paddingHorizontal: 16,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  neutralBtnPressed: { backgroundColor: OpColors.hover },
  neutralBtnText: { color: OpColors.ink, fontWeight: "600", fontSize: 15, fontFamily: OpFonts.sans },

  dangerGhostBtn: {
    minHeight: 44,
    backgroundColor: OpColors.dangerBg,
    paddingHorizontal: 16,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },
  dangerGhostText: { color: OpColors.danger, fontWeight: "700", fontSize: 15, fontFamily: OpFonts.sans },

  dangerSolidBtn: {
    minHeight: 46,
    backgroundColor: OpColors.danger,
    paddingHorizontal: 20,
    borderRadius: OpMetrics.radiusMd,
    alignItems: "center",
    justifyContent: "center",
  },

  actionRowWrap: { flexDirection: "row", flexWrap: "wrap", gap: 12 },
  commandBtn: {
    minHeight: 50,
    backgroundColor: OpColors.surface,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    paddingHorizontal: 20,
    borderRadius: OpMetrics.radiusMd,
    justifyContent: "center",
  },
  commandBtnPrimary: { backgroundColor: OpColors.selectedBg, borderColor: OpColors.selectedBg },
  commandBtnDanger: { backgroundColor: OpColors.dangerBg, borderColor: OpColors.dangerBorder },
  commandBtnText: { color: OpColors.ink, fontWeight: "700", fontSize: 16, fontFamily: OpFonts.sans },
  commandBtnPrimaryText: { color: OpColors.selectedFg },
  commandBtnDangerText: { color: OpColors.danger },

  // Leaderboard & Player History
  boardRow: {
    flexDirection: "row",
    alignItems: "center",
    gap: 16,
    paddingHorizontal: 20,
    paddingVertical: 16,
  },
  boardRank: { color: OpColors.inkMuted, fontWeight: "700", fontSize: 18, width: 44, fontFamily: OpFonts.mono },
  boardName: { color: OpColors.ink, fontWeight: "700", fontSize: 17, fontFamily: OpFonts.sans },
  boardScore: { color: OpColors.ink, fontWeight: "700", fontSize: 20, fontFamily: OpFonts.mono, marginRight: 8 },

  searchBar: { flexDirection: "row", gap: 10 },
  searchInput: {
    flex: 1,
    minHeight: 48,
    backgroundColor: OpColors.surface,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 16,
    color: OpColors.ink,
    fontSize: 16,
    fontFamily: OpFonts.sans,
  },
  historyRow: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    gap: 16,
    paddingHorizontal: 20,
    paddingVertical: 16,
  },
  historyOutcomeCol: { alignItems: "flex-end", gap: 6 },
  historyScore: { color: OpColors.ink, fontSize: 17, fontWeight: "700", fontFamily: OpFonts.mono },
  prizeTag: {
    backgroundColor: OpColors.accentSoft,
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: OpMetrics.radiusSm,
  },
  prizeTagText: { color: OpColors.accent, fontSize: 13, fontWeight: "700", fontFamily: OpFonts.sans },

  // System Key-Values
  kvRow: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 12,
    paddingHorizontal: 20,
    paddingVertical: 16,
  },
  kvKey: { color: OpColors.inkSecondary, fontSize: 16, fontWeight: "600", fontFamily: OpFonts.sans },
  kvVal: { color: OpColors.ink, fontSize: 16, fontWeight: "600", fontFamily: OpFonts.sans },
  kvValMono: { color: OpColors.ink, fontSize: 15, fontFamily: OpFonts.mono },

  metaText: { color: OpColors.inkMuted, fontSize: 14, fontFamily: OpFonts.sans },
  emptySheet: { padding: 28, alignItems: "center" },
  emptyState: { color: OpColors.inkSecondary, fontSize: 16, fontFamily: OpFonts.sans },

  // Toast
  toast: {
    position: "absolute",
    bottom: 24,
    right: 24,
    backgroundColor: OpColors.ink,
    paddingHorizontal: 20,
    paddingVertical: 14,
    borderRadius: OpMetrics.radiusMd,
  },
  toastText: { color: OpColors.inkInverse, fontWeight: "600", fontSize: 15, fontFamily: OpFonts.sans },

  // Modals
  modalBackdrop: {
    flex: 1,
    backgroundColor: "rgba(22, 22, 21, 0.45)",
    justifyContent: "center",
    alignItems: "center",
    padding: 24,
  },
  modalSheet: {
    width: "100%",
    maxWidth: 480,
    backgroundColor: OpColors.surface,
    borderColor: OpColors.line,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusLg,
    padding: 28,
    gap: 20,
  },
  modalTitle: { color: OpColors.ink, fontSize: 22, fontWeight: "700", fontFamily: OpFonts.sans },
  modalBody: { color: OpColors.inkSecondary, fontSize: 16, lineHeight: 23, fontFamily: OpFonts.sans },
  modalInput: {
    minHeight: 48,
    backgroundColor: OpColors.ground,
    borderColor: OpColors.lineStrong,
    borderWidth: 1,
    borderRadius: OpMetrics.radiusMd,
    paddingHorizontal: 14,
    color: OpColors.ink,
    fontSize: 17,
    fontFamily: OpFonts.mono,
  },
  modalActions: { flexDirection: "row", justifyContent: "flex-end", gap: 12, marginTop: 4 },

  assetGrid: { flexDirection: "row", flexWrap: "wrap", gap: 12 },
  assetTile: {
    width: 118,
    padding: 10,
    borderRadius: OpMetrics.radiusMd,
    backgroundColor: OpColors.ground,
    borderWidth: 2,
    borderColor: "transparent",
    alignItems: "center",
    gap: 8,
  },
  assetTileSelected: { borderColor: OpColors.accent, backgroundColor: OpColors.accentSoft },
  assetTileImg: { width: 84, height: 84, borderRadius: OpMetrics.radiusSm, backgroundColor: OpColors.surface },
  assetTileLabel: { color: OpColors.ink, fontSize: 13, fontWeight: "600", fontFamily: OpFonts.sans },
});
