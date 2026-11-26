import * as ed from "@noble/ed25519";
import { sha256 } from "@noble/hashes/sha256";
import { sha512 } from "@noble/hashes/sha512";
import type { KeyValueStorage } from "./storage.js";

// Required by @noble/ed25519 v2 in environments without sync WebCrypto sha512.
ed.etc.sha512Sync = (...m) => sha512(ed.etc.concatBytes(...m));

const KEYPAIR_STORAGE_KEY = "corekiosk.client_ed25519_keypair";
const PINNED_SERVERS_KEY = "corekiosk.pinned_servers";

export interface ClientKeyPair {
  deviceId: string;
  privateKey: Uint8Array;
  publicKey: Uint8Array;
  publicKeyBase64: string;
}

export interface PinnedServer {
  serverId: string;
  serverPub: string;
  lastIp: string;
  pairedAtMs: number;
}

export function bytesToBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin);
}

export function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function deviceIdFromPublicKey(pub: Uint8Array): string {
  return bytesToHex(sha256(pub).slice(0, 8));
}

export async function loadOrCreateClientKeyPair(storage: KeyValueStorage): Promise<ClientKeyPair> {
  const existing = await storage.getItem(KEYPAIR_STORAGE_KEY);
  if (existing) {
    const parsed = JSON.parse(existing) as { priv: string; pub: string };
    const privateKey = base64ToBytes(parsed.priv);
    const publicKey = base64ToBytes(parsed.pub);
    return {
      deviceId: deviceIdFromPublicKey(publicKey),
      privateKey,
      publicKey,
      publicKeyBase64: parsed.pub,
    };
  }
  const privateKey = ed.utils.randomPrivateKey();
  const publicKey = await ed.getPublicKeyAsync(privateKey);
  const publicKeyBase64 = bytesToBase64(publicKey);
  await storage.setItem(
    KEYPAIR_STORAGE_KEY,
    JSON.stringify({ priv: bytesToBase64(privateKey), pub: publicKeyBase64 })
  );
  return {
    deviceId: deviceIdFromPublicKey(publicKey),
    privateKey,
    publicKey,
    publicKeyBase64,
  };
}

export async function signUtf8(privateKey: Uint8Array, message: string): Promise<string> {
  const sig = await ed.signAsync(new TextEncoder().encode(message), privateKey);
  return bytesToBase64(sig);
}

export async function verifyUtf8(publicKeyBase64: string, message: string, sigBase64: string): Promise<boolean> {
  try {
    return await ed.verifyAsync(
      base64ToBytes(sigBase64),
      new TextEncoder().encode(message),
      base64ToBytes(publicKeyBase64)
    );
  } catch {
    return false;
  }
}

export async function loadPinnedServers(storage: KeyValueStorage): Promise<Record<string, PinnedServer>> {
  const raw = await storage.getItem(PINNED_SERVERS_KEY);
  if (!raw) return {};
  try {
    return JSON.parse(raw) as Record<string, PinnedServer>;
  } catch {
    return {};
  }
}

export async function savePinnedServer(storage: KeyValueStorage, server: PinnedServer): Promise<void> {
  const all = await loadPinnedServers(storage);
  all[server.serverId] = server;
  await storage.setItem(PINNED_SERVERS_KEY, JSON.stringify(all));
}
