import type { DiscoverySource } from "@corekiosk/client";

export interface NativeDiscoveryOffer {
  serverId: string;
  name?: string;
  appName?: string;
  host: string;
  port: number;
  source: DiscoverySource;
  version?: string;
  protocolVersion?: number;
  lastSeenMs?: number;
}

export interface NativeScanOptions {
  timeoutMs: number;
  methods: DiscoverySource[];
}

export interface NativeDiscoveryModule {
  startScan(options: NativeScanOptions): Promise<NativeDiscoveryOffer[]>;
  stopScan(): Promise<void>;
  addListener(
    eventName: "onServerFound" | "onScanState",
    listener: (event: unknown) => void
  ): { remove(): void };
}
