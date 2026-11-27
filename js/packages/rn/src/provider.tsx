import React, { createContext, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { AppState } from "react-native";
import {
  KioskApiClient,
  KioskSocket,
  KioskStore,
  MemoryStorage,
  discoveryAddress,
  loadOrCreateClientKeyPair,
  loadPinnedServers,
  mergeDiscoveredServers,
  rankDiscoveredServers,
  savePinnedServer,
  type ActionResult,
  type AssetMeta,
  type CatalogEntry,
  type CatalogView,
  type ClientKeyPair,
  type DeviceInfo,
  type DiscoveredServer,
  type DiscoveryAdapter,
  type KeyValueStorage,
  type KioskState,
  type PinnedServer,
  type RuleEntry,
  type RulesetView,
  type SubmissionRecord,
  type DeviceRecord,
} from "@corekiosk/client";
import {
  LocalTelemetryOutbox,
  TelemetryMuleSync,
  startDeviceCollectors,
  type BatteryModuleLike,
  type NetInfoModuleLike,
  type SQLiteDriver,
} from "@corekiosk/telemetry-rn";

const LAST_HOST_KEY = "corekiosk.last_host";
const LAST_SERVER_ID_KEY = "corekiosk.last_server_id";
const AUTO_RECONNECT_RETRY_MS = 30_000;

export interface KioskContextValue {
  state: KioskState;
  ready: boolean;
  paired: boolean;
  serverUrl: string;
  clientDeviceId: string;
  pinnedServer: PinnedServer | null;
  pinnedServerIds: string[];
  operatorUnlocked: boolean;
  operatorUsername: string | null;
  showConnect: boolean;
  showOperator: boolean;
  discovered: DiscoveredServer[];
  discovering: boolean;
  discoveryError: string | null;
  openConnect(): void;
  closeConnect(): void;
  openOperator(): void;
  closeOperator(): void;
  scanForServers(timeoutMs?: number): Promise<DiscoveredServer[]>;
  connectToDiscovered(server: DiscoveredServer, pairCode?: string): Promise<void>;
  connectOrPair(address: string, pairCode?: string): Promise<void>;
  unpair(): Promise<void>;
  submitForm(fields: Record<string, unknown>, opts?: { formId?: string; armRound?: boolean; requestId?: string }): Promise<Record<string, unknown>>;
  cancelActiveRound(): Promise<void>;
  acknowledgeResults(): Promise<void>;
  loginOperator(username: string, password: string): Promise<void>;
  logoutOperator(): void;
  patchConfig(values: Record<string, unknown>, live?: boolean): Promise<void>;
  triggerAction(actionId: string, params?: Record<string, unknown>): Promise<ActionResult>;
  deleteBoardRow(boardId: string, entryId: string): Promise<void>;
  assetUrl(assetId?: string | null): string;
  fetchAssets(): Promise<AssetMeta[]>;
  uploadAssetFile(file: Blob, filename?: string): Promise<AssetMeta>;
  uploadAssetBase64(filename: string, dataBase64: string): Promise<AssetMeta>;
  addCatalogEntry(catalogId: string, entry: { id?: string; enabled?: boolean; fields: Record<string, unknown> }): Promise<CatalogEntry>;
  patchCatalogEntry(catalogId: string, entryId: string, patch: { enabled?: boolean; order?: number; fields?: Record<string, unknown> }): Promise<CatalogEntry>;
  deleteCatalogEntry(catalogId: string, entryId: string): Promise<CatalogView>;
  saveRuleset(rulesetId: string, rules: RuleEntry[]): Promise<RulesetView>;
  fetchSubmissions(q?: string, limit?: number, offset?: number): Promise<{ items: SubmissionRecord[]; total: number }>;
  fetchDevices(): Promise<DeviceRecord[]>;
  serverNowMs(): number;
}

export const KioskContext = createContext<KioskContextValue | null>(null);

export interface KioskProviderProps {
  children: React.ReactNode;
  storage?: KeyValueStorage;
  deviceInfo: DeviceInfo;
  sqliteDb?: SQLiteDriver;
  netInfo?: NetInfoModuleLike;
  battery?: BatteryModuleLike;
  telemetryIngestUrl?: string;
  telemetryApiKey?: string;
  operatorIdleLockMs?: number;
  discovery?: DiscoveryAdapter;
  discoveryTimeoutMs?: number;
  discoveryFallbackMs?: number;
  autoReconnect?: boolean;
}

function normalizeBaseUrl(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, "");
  if (!trimmed) return "";
  if (/^https?:\/\//i.test(trimmed)) return trimmed;
  return trimmed.includes(":") ? `http://${trimmed}` : `http://${trimmed}:8080`;
}

export function KioskProvider({
  children,
  storage: customStorage,
  deviceInfo,
  sqliteDb,
  netInfo,
  battery,
  telemetryIngestUrl,
  telemetryApiKey,
  operatorIdleLockMs = 5 * 60_000,
  discovery,
  discoveryTimeoutMs = 3500,
  discoveryFallbackMs = 2500,
  autoReconnect = true,
}: KioskProviderProps) {
  const storage = useMemo(() => customStorage ?? new MemoryStorage(), [customStorage]);
  const storeRef = useRef<KioskStore>(new KioskStore());
  const apiRef = useRef<KioskApiClient>(new KioskApiClient(""));
  const socketRef = useRef<KioskSocket | null>(null);
  const keyPairRef = useRef<ClientKeyPair | null>(null);
  const deviceTokenRef = useRef<string>("");
  const lastSchemaHashRef = useRef<string>("");
  const idleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const state = useSyncExternalStore(storeRef.current.subscribe, storeRef.current.getState, storeRef.current.getState);
  const [ready, setReady] = useState(false);
  const [serverUrl, setServerUrl] = useState("");
  const [clientDeviceId, setClientDeviceId] = useState("");
  const [pinnedServer, setPinnedServer] = useState<PinnedServer | null>(null);
  const [operatorUnlocked, setOperatorUnlocked] = useState(false);
  const [showConnect, setShowConnect] = useState(false);
  const [showOperator, setShowOperator] = useState(false);
  const [discovered, setDiscovered] = useState<DiscoveredServer[]>([]);
  const [discovering, setDiscovering] = useState(false);
  const [discoveryError, setDiscoveryError] = useState<string | null>(null);
  const [pinnedServerIds, setPinnedServerIds] = useState<string[]>([]);

  const discoveredRef = useRef<DiscoveredServer[]>([]);
  const pinnedServerRef = useRef<PinnedServer | null>(null);
  const pinnedServerIdsRef = useRef<string[]>([]);
  const showConnectRef = useRef(false);
  const autoConnectingRef = useRef(false);
  const autoAttemptsRef = useRef<Map<string, number>>(new Map());
  const discoveryFallbackTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const discoveryRetryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    showConnectRef.current = showConnect;
  }, [showConnect]);

  const rememberPinnedIds = useCallback((ids: string[]) => {
    pinnedServerIdsRef.current = ids;
    setPinnedServerIds(ids);
  }, []);

  const applyDiscovered = useCallback((servers: DiscoveredServer[]) => {
    const merged = mergeDiscoveredServers(discoveredRef.current, servers);
    discoveredRef.current = merged;
    setDiscovered(merged);
    return merged;
  }, []);

  const scanForServers = useCallback(
    async (timeoutMs = discoveryTimeoutMs): Promise<DiscoveredServer[]> => {
      if (!discovery) return discoveredRef.current;
      setDiscovering(true);
      setDiscoveryError(null);
      try {
        const results = await discovery.startScan({ timeoutMs });
        return applyDiscovered(results);
      } catch (err) {
        setDiscoveryError(err instanceof Error ? err.message : String(err));
        return discoveredRef.current;
      } finally {
        setDiscovering(false);
      }
    },
    [applyDiscovered, discovery, discoveryTimeoutMs]
  );

  useEffect(() => {
    if (!discovery) return;
    const unsubscribe = discovery.subscribe((servers) => {
      applyDiscovered(servers);
    });
    return () => {
      unsubscribe();
      void discovery.stopScan().catch(() => undefined);
    };
  }, [applyDiscovered, discovery]);

  const logoutOperator = useCallback(() => {
    if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
    apiRef.current.clearOperatorToken();
    socketRef.current?.dropOperator();
    setOperatorUnlocked(false);
    storeRef.current.patch({ operatorUsername: null });
  }, []);

  const bumpOperatorIdleTimer = useCallback(() => {
    if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
    if (operatorIdleLockMs > 0) {
      idleTimerRef.current = setTimeout(() => {
        logoutOperator();
        setShowOperator(false);
      }, operatorIdleLockMs);
    }
  }, [logoutOperator, operatorIdleLockMs]);

  const startSocket = useCallback(
    (url: string, kp: ClientKeyPair, pinned: PinnedServer) => {
      socketRef.current?.disconnect();
      apiRef.current.setBaseUrl(url);

      const sock = new KioskSocket(
        {
          baseUrl: url,
          keyPair: kp,
          pinnedServerPub: pinned.serverPub,
          deviceInfo,
        },
        {
          onStatus: (connection) => storeRef.current.patch({ connection }),
          onAuthOk: async (payload) => {
            deviceTokenRef.current = payload.device_token;
            apiRef.current.setDeviceToken(payload.device_token);
            storeRef.current.patch({
              snapshot: payload.snapshot,
              configRevision: payload.config_rev,
              values: payload.values,
              runtimeValues: payload.runtime_values,
              settings: payload.settings,
              boards: payload.snapshot.boards ?? {},
              catalogs: payload.snapshot.catalogs ?? {},
              rulesets: payload.snapshot.rulesets ?? {},
            });
            lastSchemaHashRef.current = payload.snapshot.schema_hash;
            try {
              const schema = await apiRef.current.schema();
              storeRef.current.patch({ schema });
            } catch {
              // Retry on next snapshot
            }
          },
          onSnapshot: async (snapshot) => {
            storeRef.current.patch({
              snapshot,
              boards: snapshot.boards ?? storeRef.current.getState().boards,
              catalogs: snapshot.catalogs ?? storeRef.current.getState().catalogs,
              rulesets: snapshot.rulesets ?? storeRef.current.getState().rulesets,
            });
            if (snapshot.schema_hash && snapshot.schema_hash !== lastSchemaHashRef.current) {
              lastSchemaHashRef.current = snapshot.schema_hash;
              try {
                const schema = await apiRef.current.schema();
                storeRef.current.patch({ schema });
              } catch {}
            }
          },
          onConfig: (cfg) => {
            storeRef.current.patch({
              configRevision: cfg.revision,
              values: cfg.values,
              runtimeValues: cfg.runtime_values,
              settings: cfg.settings,
            });
          },
          onBoardUpdated: (boardId, board) => {
            const cur = storeRef.current.getState().boards;
            storeRef.current.patch({ boards: { ...cur, [boardId]: board } });
          },
          onCatalogUpdated: (catalogId, catalog) => {
            const cur = storeRef.current.getState().catalogs;
            storeRef.current.patch({ catalogs: { ...cur, [catalogId]: catalog } });
          },
          onRulesetUpdated: (rulesetId, ruleset) => {
            const cur = storeRef.current.getState().rulesets;
            storeRef.current.patch({ rulesets: { ...cur, [rulesetId]: ruleset } });
          },
          onClockOffset: (clockOffsetMs, rttMs) => {
            storeRef.current.patch({ clockOffsetMs, rttMs });
          },
        }
      );
      socketRef.current = sock;
      sock.connect();
    },
    [deviceInfo]
  );

  // Boot: load persistent Ed25519 client keypair & auto-reconnect to pinned server (zero PIN!).
  useEffect(() => {
    let alive = true;
    let collectorStop: (() => void) | null = null;
    let muleSync: TelemetryMuleSync | null = null;

    (async () => {
      try {
        const kp = await loadOrCreateClientKeyPair(storage);
        if (!alive) return;
        keyPairRef.current = kp;
        setClientDeviceId(kp.deviceId);

        const savedHost = await storage.getItem(LAST_HOST_KEY);
        const savedServerId = await storage.getItem(LAST_SERVER_ID_KEY);
        const pinnedMap = await loadPinnedServers(storage);
        const pinned = savedServerId ? pinnedMap[savedServerId] ?? null : null;
        pinnedServerRef.current = pinned;
        rememberPinnedIds(Object.keys(pinnedMap));

        if (savedHost && pinned) {
          setServerUrl(savedHost);
          setPinnedServer(pinned);
          startSocket(savedHost, kp, pinned);
          if (discovery && autoReconnect) {
            discoveryFallbackTimerRef.current = setTimeout(() => {
              if (storeRef.current.getState().connection !== "connected") {
                void scanForServers();
              }
            }, discoveryFallbackMs);
          }
        } else if (discovery && autoReconnect) {
          // First install: populate the pairing screen immediately.
          void scanForServers();
        }

        if (sqliteDb) {
          try {
            const bootId = Math.random().toString(16).slice(2, 10);
            const outbox = new LocalTelemetryOutbox(sqliteDb, kp.deviceId, bootId);
            const handles = startDeviceCollectors({ outbox, netInfo, battery });
            collectorStop = handles.stop;
            muleSync = new TelemetryMuleSync({
              outbox,
              keyPair: kp,
              getServerBaseUrl: () => serverUrl || savedHost || "",
              getDeviceToken: () => deviceTokenRef.current,
              getServerId: () => pinnedServer?.serverId || pinned?.serverId || "",
              getAppName: () => storeRef.current.getState().schema?.app_name || "",
              isServerCloudOnline: () => Boolean(storeRef.current.getState().snapshot?.cloud_online),
              isTabletInternetReachable: () => handles.isInternetReachable(),
              telemetryIngestUrl,
              telemetryApiKey,
            });
            muleSync.start();
          } catch (telErr) {
            console.warn("[corekiosk] telemetry boot warning:", telErr);
          }
        }
      } catch (err) {
        console.error("[corekiosk] boot failed:", err);
      } finally {
        if (alive) setReady(true);
      }
    })();

    return () => {
      alive = false;
      collectorStop?.();
      muleSync?.stop();
      socketRef.current?.disconnect();
      if (idleTimerRef.current) clearTimeout(idleTimerRef.current);
      if (discoveryFallbackTimerRef.current) clearTimeout(discoveryFallbackTimerRef.current);
      if (discoveryRetryTimerRef.current) clearTimeout(discoveryRetryTimerRef.current);
    };
  }, [
    storage,
    sqliteDb,
    netInfo,
    battery,
    telemetryIngestUrl,
    telemetryApiKey,
    startSocket,
    discovery,
    autoReconnect,
    discoveryFallbackMs,
    scanForServers,
    rememberPinnedIds,
  ]);

  const connectOrPair = useCallback(
    async (address: string, pairCode?: string) => {
      const kp = keyPairRef.current ?? (await loadOrCreateClientKeyPair(storage));
      keyPairRef.current = kp;
      const url = normalizeBaseUrl(address);
      if (!url) throw new Error("Enter the kiosk server IP address.");

      apiRef.current.setBaseUrl(url);
      const health = await apiRef.current.health();
      const pinnedMap = await loadPinnedServers(storage);
      const existingPin = pinnedMap[health.server_id];

      if (existingPin && existingPin.serverPub === health.server_pub && !pairCode?.trim()) {
        // Already paired with this server -> IP-only zero-PIN reconnect!
        const res = await apiRef.current.authenticateWithKey(kp, existingPin.serverPub, deviceInfo);
        deviceTokenRef.current = res.device_token;
        const updatedPin: PinnedServer = { ...existingPin, lastIp: url };
        await savePinnedServer(storage, updatedPin);
        await storage.setItem(LAST_HOST_KEY, url);
        await storage.setItem(LAST_SERVER_ID_KEY, updatedPin.serverId);
        pinnedServerRef.current = updatedPin;
        rememberPinnedIds(Array.from(new Set([...pinnedServerIdsRef.current, updatedPin.serverId])));
        setServerUrl(url);
        setPinnedServer(updatedPin);
        setShowConnect(false);
        startSocket(url, kp, updatedPin);
        return;
      }

      if (!pairCode?.trim()) {
        throw new Error("First-time pairing requires the 6-digit code shown on the kiosk.");
      }

      const res = await apiRef.current.pair(kp, pairCode.trim(), deviceInfo);
      deviceTokenRef.current = res.device_token;
      const pin: PinnedServer = {
        serverId: res.server_id,
        serverPub: res.server_pub,
        lastIp: url,
        pairedAtMs: Date.now(),
      };
      await savePinnedServer(storage, pin);
      await storage.setItem(LAST_HOST_KEY, url);
      await storage.setItem(LAST_SERVER_ID_KEY, pin.serverId);
      pinnedServerRef.current = pin;
      rememberPinnedIds(Array.from(new Set([...pinnedServerIdsRef.current, pin.serverId])));
      setServerUrl(url);
      setPinnedServer(pin);
      setShowConnect(false);
      startSocket(url, kp, pin);
    },
    [deviceInfo, rememberPinnedIds, startSocket, storage]
  );

  const connectToDiscovered = useCallback(
    async (server: DiscoveredServer, pairCode?: string) => {
      const hosts = server.hosts.length > 0 ? server.hosts : [server.host];
      let lastError: unknown = null;
      for (const host of hosts) {
        try {
          await connectOrPair(discoveryAddress({ host, port: server.port }), pairCode);
          return;
        } catch (err) {
          lastError = err;
        }
      }
      throw lastError instanceof Error ? lastError : new Error("No reachable address for this kiosk.");
    },
    [connectOrPair]
  );

  const attemptAutoReconnect = useCallback(
    async (servers: DiscoveredServer[]) => {
      if (autoConnectingRef.current) return;
      if (showConnectRef.current) return;
      if (storeRef.current.getState().connection === "connected") return;
      const pin = pinnedServerRef.current;
      if (!pin) return;

      const candidates = rankDiscoveredServers(servers, pin.serverId).filter(
        (server) => server.serverId === pin.serverId
      );
      if (candidates.length === 0) return;

      autoConnectingRef.current = true;
      try {
        for (const server of candidates) {
          const hosts = server.hosts.length > 0 ? server.hosts : [server.host];
          for (const host of hosts) {
            const key = `${server.serverId}|${host}|${server.port}`;
            const lastAttempt = autoAttemptsRef.current.get(key) ?? 0;
            if (Date.now() - lastAttempt < AUTO_RECONNECT_RETRY_MS) continue;
            autoAttemptsRef.current.set(key, Date.now());
            try {
              await connectOrPair(discoveryAddress({ host, port: server.port }));
              return;
            } catch (err) {
              setDiscoveryError(err instanceof Error ? err.message : String(err));
            }
          }
        }
      } finally {
        autoConnectingRef.current = false;
      }
    },
    [connectOrPair]
  );

  // Auto-reconnect a pinned tablet when discovery finds its server at a new IP.
  useEffect(() => {
    if (!autoReconnect || !discovery) return;
    if (state.connection === "connected" || showConnect) return;
    if (!pinnedServerRef.current) return;
    void attemptAutoReconnect(discovered);
  }, [attemptAutoReconnect, autoReconnect, discovered, discovery, showConnect, state.connection]);

  // Retry discovery shortly after the socket drops (DHCP change, Wi-Fi roam, server restart).
  useEffect(() => {
    if (!autoReconnect || !discovery) return;
    if (state.connection === "connected") return;
    if (!pinnedServerRef.current) return;
    if (discoveryRetryTimerRef.current) clearTimeout(discoveryRetryTimerRef.current);
    discoveryRetryTimerRef.current = setTimeout(() => {
      void scanForServers();
    }, 1500);
    return () => {
      if (discoveryRetryTimerRef.current) clearTimeout(discoveryRetryTimerRef.current);
    };
  }, [autoReconnect, discovery, scanForServers, state.connection]);

  // Foreground + network recovery: scan again if still disconnected.
  useEffect(() => {
    if (!autoReconnect || !discovery) return;
    const subscription = AppState.addEventListener("change", (next) => {
      if (next === "active" && storeRef.current.getState().connection !== "connected") {
        void scanForServers();
      }
    });
    return () => subscription.remove();
  }, [autoReconnect, discovery, scanForServers]);

  useEffect(() => {
    if (!autoReconnect || !discovery || !netInfo) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const unsubscribe = netInfo.addEventListener((netState) => {
      if (!netState.isConnected) return;
      if (storeRef.current.getState().connection === "connected") return;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => void scanForServers(), 1000);
    });
    return () => {
      unsubscribe();
      if (timer) clearTimeout(timer);
    };
  }, [autoReconnect, discovery, netInfo, scanForServers]);

  const unpair = useCallback(async () => {
    socketRef.current?.disconnect();
    logoutOperator();
    await storage.removeItem(LAST_HOST_KEY);
    await storage.removeItem(LAST_SERVER_ID_KEY);
    pinnedServerRef.current = null;
    setPinnedServer(null);
    setServerUrl("");
    setShowOperator(false);
    setShowConnect(true);
    setDiscovered([]);
    discoveredRef.current = [];
  }, [logoutOperator, storage]);

  const submitForm = useCallback(
    async (fields: Record<string, unknown>, opts?: { formId?: string; armRound?: boolean; requestId?: string }) => {
      const reqId = opts?.requestId || `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
      return apiRef.current.submitForm(reqId, fields, opts?.formId ?? "", opts?.armRound ?? true);
    },
    []
  );

  const cancelActiveRound = useCallback(async () => {
    const id = storeRef.current.getState().snapshot?.round?.id;
    if (id) await apiRef.current.cancelRound(id);
  }, []);

  const acknowledgeResults = useCallback(async () => {
    const id = storeRef.current.getState().snapshot?.round?.id || "current";
    await apiRef.current.acknowledgeRound(id);
  }, []);

  const loginOperator = useCallback(
    async (username: string, password: string) => {
      const res = await apiRef.current.operatorLogin(username, password);
      socketRef.current?.elevateOperator(res.access_token);
      setOperatorUnlocked(true);
      storeRef.current.patch({ operatorUsername: res.operator.username });
      bumpOperatorIdleTimer();
    },
    [bumpOperatorIdleTimer]
  );

  const patchConfig = useCallback(
    async (values: Record<string, unknown>, live = true) => {
      bumpOperatorIdleTimer();
      if (live) {
        socketRef.current?.sendConfigPatch(values);
        return;
      }
      const rev = storeRef.current.getState().configRevision;
      await apiRef.current.patchConfig(values, rev);
    },
    [bumpOperatorIdleTimer]
  );

  const triggerAction = useCallback(
    async (actionId: string, params: Record<string, unknown> = {}) => {
      bumpOperatorIdleTimer();
      return apiRef.current.triggerAction(actionId, params);
    },
    [bumpOperatorIdleTimer]
  );

  const deleteBoardRow = useCallback(
    async (boardId: string, entryId: string) => {
      bumpOperatorIdleTimer();
      await apiRef.current.deleteBoardRow(boardId, entryId);
    },
    [bumpOperatorIdleTimer]
  );

  const assetUrl = useCallback((assetId?: string | null) => {
    if (!assetId) return "";
    return apiRef.current.assetUrl(assetId);
  }, []);

  const fetchAssets = useCallback(async () => {
    bumpOperatorIdleTimer();
    const res = await apiRef.current.listAssets();
    return res.assets;
  }, [bumpOperatorIdleTimer]);

  const uploadAssetFile = useCallback(
    async (file: Blob, filename?: string) => {
      bumpOperatorIdleTimer();
      const res = await apiRef.current.uploadAssetFile(file, filename);
      return res.asset;
    },
    [bumpOperatorIdleTimer]
  );

  const uploadAssetBase64 = useCallback(
    async (filename: string, dataBase64: string) => {
      bumpOperatorIdleTimer();
      const res = await apiRef.current.uploadAssetBase64(filename, dataBase64);
      return res.asset;
    },
    [bumpOperatorIdleTimer]
  );

  const addCatalogEntry = useCallback(
    async (catalogId: string, entry: { id?: string; enabled?: boolean; fields: Record<string, unknown> }) => {
      bumpOperatorIdleTimer();
      const res = await apiRef.current.addCatalogEntry(catalogId, entry);
      const cur = storeRef.current.getState().catalogs;
      storeRef.current.patch({ catalogs: { ...cur, [catalogId]: res.catalog } });
      return res.entry;
    },
    [bumpOperatorIdleTimer]
  );

  const patchCatalogEntry = useCallback(
    async (catalogId: string, entryId: string, patch: { enabled?: boolean; order?: number; fields?: Record<string, unknown> }) => {
      bumpOperatorIdleTimer();
      const res = await apiRef.current.patchCatalogEntry(catalogId, entryId, patch);
      const cur = storeRef.current.getState().catalogs;
      storeRef.current.patch({ catalogs: { ...cur, [catalogId]: res.catalog } });
      return res.entry;
    },
    [bumpOperatorIdleTimer]
  );

  const deleteCatalogEntry = useCallback(
    async (catalogId: string, entryId: string) => {
      bumpOperatorIdleTimer();
      const view = await apiRef.current.deleteCatalogEntry(catalogId, entryId);
      const cur = storeRef.current.getState().catalogs;
      storeRef.current.patch({ catalogs: { ...cur, [catalogId]: view } });
      return view;
    },
    [bumpOperatorIdleTimer]
  );

  const saveRuleset = useCallback(
    async (rulesetId: string, rules: RuleEntry[]) => {
      bumpOperatorIdleTimer();
      const view = await apiRef.current.saveRuleset(rulesetId, rules);
      const cur = storeRef.current.getState().rulesets;
      storeRef.current.patch({ rulesets: { ...cur, [rulesetId]: view } });
      return view;
    },
    [bumpOperatorIdleTimer]
  );

  const fetchSubmissions = useCallback(
    async (q = "", limit = 50, offset = 0) => {
      bumpOperatorIdleTimer();
      return apiRef.current.listSubmissions({ q, limit, offset });
    },
    [bumpOperatorIdleTimer]
  );

  const fetchDevices = useCallback(async () => {
    bumpOperatorIdleTimer();
    const res = await apiRef.current.listDevices();
    return res.devices;
  }, [bumpOperatorIdleTimer]);

  const value = useMemo<KioskContextValue>(
    () => ({
      state,
      ready,
      paired: Boolean(pinnedServer && serverUrl),
      serverUrl,
      clientDeviceId,
      pinnedServer,
      pinnedServerIds,
      operatorUnlocked,
      operatorUsername: state.operatorUsername,
      showConnect,
      showOperator,
      discovered,
      discovering,
      discoveryError,
      openConnect: () => {
        setShowConnect(true);
        void scanForServers();
      },
      closeConnect: () => setShowConnect(false),
      openOperator: () => setShowOperator(true),
      closeOperator: () => {
        setShowOperator(false);
        logoutOperator();
      },
      scanForServers,
      connectToDiscovered,
      connectOrPair,
      unpair,
      submitForm,
      cancelActiveRound,
      acknowledgeResults,
      loginOperator,
      logoutOperator,
      patchConfig,
      triggerAction,
      deleteBoardRow,
      assetUrl,
      fetchAssets,
      uploadAssetFile,
      uploadAssetBase64,
      addCatalogEntry,
      patchCatalogEntry,
      deleteCatalogEntry,
      saveRuleset,
      fetchSubmissions,
      fetchDevices,
      serverNowMs: () => storeRef.current.serverNowMs(),
    }),
    [
      state,
      ready,
      pinnedServer,
      pinnedServerIds,
      serverUrl,
      clientDeviceId,
      operatorUnlocked,
      discovered,
      discovering,
      discoveryError,
      scanForServers,
      connectToDiscovered,
      showConnect,
      showOperator,
      logoutOperator,
      connectOrPair,
      unpair,
      submitForm,
      cancelActiveRound,
      acknowledgeResults,
      loginOperator,
      patchConfig,
      triggerAction,
      deleteBoardRow,
      assetUrl,
      fetchAssets,
      uploadAssetFile,
      uploadAssetBase64,
      addCatalogEntry,
      patchCatalogEntry,
      deleteCatalogEntry,
      saveRuleset,
      fetchSubmissions,
      fetchDevices,
    ]
  );

  return <KioskContext.Provider value={value}>{children}</KioskContext.Provider>;
}
