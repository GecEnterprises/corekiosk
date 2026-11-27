import {
  mergeDiscoveredServers,
  type DiscoveredServer,
  type DiscoveryAdapter,
  type DiscoveryAdapterOptions,
  type DiscoveryScanOptions,
  type DiscoverySource,
} from "@corekiosk/client";
import { requireOptionalNativeModule } from "expo-modules-core";
import type { NativeDiscoveryModule, NativeDiscoveryOffer } from "./types.js";

export type { NativeDiscoveryModule, NativeDiscoveryOffer, NativeScanOptions } from "./types.js";

const DEFAULT_METHODS: DiscoverySource[] = ["udp", "mdns", "ssdp"];
const DEFAULT_TIMEOUT_MS = 3500;

/**
 * Creates a native LAN discovery adapter. On Web (or any platform where the native
 * module is not linked) this returns an empty no-op adapter, so callers can keep the
 * manual IP + pairing-code path working unchanged.
 */
export function createDiscoveryAdapter(options: DiscoveryAdapterOptions = {}): DiscoveryAdapter {
  const native = requireOptionalNativeModule<NativeDiscoveryModule>("CorekioskDiscovery");
  const defaultMethods = options.methods ?? DEFAULT_METHODS;
  const defaultTimeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;

  let servers: DiscoveredServer[] = [];
  const listeners = new Set<(servers: DiscoveredServer[]) => void>();
  const subscriptions: Array<{ remove(): void }> = [];

  const emit = () => {
    const snapshot = servers.slice();
    for (const listener of listeners) listener(snapshot);
  };

  const mergeOffers = (offers: readonly NativeDiscoveryOffer[] | null | undefined) => {
    if (!offers || offers.length === 0) return;
    const incoming: DiscoveredServer[] = [];
    for (const offer of offers) {
      if (!offer?.serverId || !offer.host) continue;
      incoming.push({
        serverId: offer.serverId,
        name: offer.name || offer.appName || "CoreKiosk",
        host: offer.host,
        hosts: [offer.host],
        port: offer.port || 8080,
        sources: [offer.source],
        appName: offer.appName,
        version: offer.version,
        protocolVersion: offer.protocolVersion,
        lastSeenMs: offer.lastSeenMs || Date.now(),
      });
    }
    if (incoming.length === 0) return;
    servers = mergeDiscoveredServers(servers, incoming);
    emit();
  };

  if (native) {
    subscriptions.push(
      native.addListener("onServerFound", (event) => {
        const payload = event as { server?: NativeDiscoveryOffer } | NativeDiscoveryOffer | null;
        if (!payload) return;
        const offer = "server" in payload && payload.server ? payload.server : (payload as NativeDiscoveryOffer);
        mergeOffers([offer]);
      })
    );
  }

  return {
    async startScan(scanOptions?: DiscoveryScanOptions): Promise<DiscoveredServer[]> {
      if (!native) return servers.slice();
      const timeoutMs = scanOptions?.timeoutMs ?? defaultTimeoutMs;
      const methods = scanOptions?.methods ?? defaultMethods;
      const offers = await native.startScan({ timeoutMs, methods });
      mergeOffers(offers);
      return servers.slice();
    },

    async stopScan(): Promise<void> {
      if (!native) return;
      try {
        await native.stopScan();
      } catch (err) {
        options.onError?.(err);
      }
    },

    subscribe(listener) {
      listeners.add(listener);
      listener(servers.slice());
      return () => {
        listeners.delete(listener);
      };
    },

    getServers() {
      return servers.slice();
    },

    dispose() {
      listeners.clear();
      for (const subscription of subscriptions) subscription.remove();
      subscriptions.length = 0;
      if (native) {
        void native.stopScan().catch((err) => options.onError?.(err));
      }
    },
  };
}
