/**
 * Platform-agnostic LAN discovery types and merge/rank helpers.
 *
 * Concrete transports (UDP broadcast, mDNS, SSDP) live in @corekiosk/discovery-rn.
 * Everything here is pure TypeScript so it can be unit-tested and used on Web/Node.
 */

export type DiscoverySource = "udp" | "mdns" | "ssdp";

export interface DiscoveredServer {
  /** Stable corekiosk server identity. Never trust this without a /v1/health check. */
  serverId: string;
  name: string;
  /** Best host to try first. */
  host: string;
  /** All addresses seen for this server (IPv4 and/or IPv6). */
  hosts: string[];
  port: number;
  sources: DiscoverySource[];
  appName?: string;
  version?: string;
  protocolVersion?: number;
  lastSeenMs: number;
}

export interface DiscoveryScanOptions {
  timeoutMs?: number;
  methods?: DiscoverySource[];
}

export interface DiscoveryAdapter {
  /** Starts a scan and resolves with the results collected during the scan window. */
  startScan(options?: DiscoveryScanOptions): Promise<DiscoveredServer[]>;
  /** Cancels an in-flight scan. */
  stopScan(): Promise<void>;
  /** Subscribes to live results. The listener is invoked immediately with the current list. */
  subscribe(listener: (servers: DiscoveredServer[]) => void): () => void;
  /** Returns the current merged list without waiting for an event. */
  getServers(): DiscoveredServer[];
  /** Releases native listeners/locks. */
  dispose?(): void;
}

export interface DiscoveryAdapterOptions {
  methods?: DiscoverySource[];
  timeoutMs?: number;
  /** Optional logger for native/degraded-mode diagnostics. */
  onError?: (error: unknown) => void;
}

export function discoverySourcePriority(source: DiscoverySource): number {
  switch (source) {
    case "mdns":
      return 3;
    case "udp":
      return 2;
    case "ssdp":
      return 1;
    default:
      return 0;
  }
}

export function uniqueDiscoveryHosts(hosts: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of hosts) {
    const host = raw.trim();
    if (!host || seen.has(host)) continue;
    seen.add(host);
    out.push(host);
  }
  // Prefer private IPv4, then any IPv4, then IPv6. This keeps a tablet on the
  // venue LAN from trying a link-local/global address first.
  return out.sort((a, b) => hostRank(a) - hostRank(b));
}

function hostRank(host: string): number {
  if (isIPv4(host)) {
    if (isPrivateIPv4(host)) return 0;
    if (host.startsWith("169.254.")) return 3;
    return 1;
  }
  if (host.includes(":")) return 2;
  // Hostname / mDNS name: resolve through the OS, rank after numeric IPv4.
  return 1.5;
}

export function mergeDiscoveredServers(
  current: readonly DiscoveredServer[],
  incoming: readonly DiscoveredServer[]
): DiscoveredServer[] {
  const byId = new Map<string, DiscoveredServer>();
  const order: string[] = [];
  for (const server of [...current, ...incoming]) {
    if (!server.serverId) continue;
    const existing = byId.get(server.serverId);
    if (!existing) {
      byId.set(server.serverId, normalizeServer(server));
      order.push(server.serverId);
      continue;
    }
    const merged: DiscoveredServer = {
      ...existing,
      name: existing.name || server.name,
      appName: existing.appName || server.appName,
      version: existing.version || server.version,
      protocolVersion: existing.protocolVersion || server.protocolVersion,
      port: existing.port || server.port,
      hosts: uniqueDiscoveryHosts([...existing.hosts, ...server.hosts, server.host]),
      sources: uniqueSources([...existing.sources, ...server.sources]),
      lastSeenMs: Math.max(existing.lastSeenMs, server.lastSeenMs),
    };
    merged.host = merged.hosts[0] ?? existing.host;
    byId.set(server.serverId, merged);
  }
  return order.map((id) => byId.get(id)!).filter(Boolean);
}

export function normalizeServer(server: DiscoveredServer): DiscoveredServer {
  const hosts = uniqueDiscoveryHosts([...(server.hosts ?? []), server.host]);
  return {
    ...server,
    name: server.name || "",
    host: hosts[0] ?? server.host,
    hosts,
    sources: uniqueSources(server.sources ?? []),
    port: server.port || 8080,
    lastSeenMs: server.lastSeenMs || Date.now(),
  };
}

function uniqueSources(sources: readonly DiscoverySource[]): DiscoverySource[] {
  const seen = new Set<DiscoverySource>();
  const out: DiscoverySource[] = [];
  for (const source of sources) {
    if (!source || seen.has(source)) continue;
    seen.add(source);
    out.push(source);
  }
  return out;
}

export function rankDiscoveredServers(
  servers: readonly DiscoveredServer[],
  pinnedServerId?: string | null,
  localIp?: string | null
): DiscoveredServer[] {
  return [...servers].sort((a, b) => scoreServer(b, pinnedServerId, localIp) - scoreServer(a, pinnedServerId, localIp));
}

function scoreServer(server: DiscoveredServer, pinnedServerId?: string | null, localIp?: string | null): number {
  let score = 0;
  if (pinnedServerId && server.serverId === pinnedServerId) score += 1000;
  if (localIp && server.hosts?.some((host) => sameSubnet24(localIp, host))) score += 100;
  score += Math.max(0, ...(server.sources ?? []).map(discoverySourcePriority)) * 10;
  // Tiny recency tiebreaker so the most recently seen server wins.
  score += Math.min(9, Math.max(0, (server.lastSeenMs || 0) / 1e12));
  return score;
}

/** Formats a host:port pair, bracketing IPv6 literals. */
export function discoveryAddress(server: Pick<DiscoveredServer, "host" | "port">): string {
  const host = server.host.includes(":") && !server.host.startsWith("[") ? `[${server.host}]` : server.host;
  return `${host}:${server.port}`;
}

export function sameSubnet24(a: string, b: string): boolean {
  if (!isIPv4(a) || !isIPv4(b)) return false;
  return a.split(".").slice(0, 3).join(".") === b.split(".").slice(0, 3).join(".");
}

export function isIPv4(host: string): boolean {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host);
}

export function isPrivateIPv4(host: string): boolean {
  if (!isIPv4(host)) return false;
  const parts = host.split(".").map(Number);
  if (parts.some((n) => !Number.isFinite(n) || n < 0 || n > 255)) return false;
  return (
    parts[0] === 10 ||
    (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
    (parts[0] === 192 && parts[1] === 168) ||
    parts[0] === 127
  );
}
