import * as ed from "@noble/ed25519";
import { sha256 } from "@noble/hashes/sha256";
import { sha512 } from "@noble/hashes/sha512";
import type { KeyValueStorage } from "./storage.js";

// Required by @noble/ed25519 v2 in environments without WebCrypto subtle.digest
// (React Native / Hermes and plain HTTP LAN browsers).
const sha512Concat = (...m: Uint8Array[]) => sha512(ed.etc.concatBytes(...m));
ed.etc.sha512Sync = sha512Concat;
ed.etc.sha512Async = (...m) => Promise.resolve(sha512Concat(...m));

const KEYPAIR_STORAGE_KEY = "corekiosk.client_ed25519_keypair";
const PINNED_SERVERS_KEY = "corekiosk.pinned_servers";
const BASE64_CHARS = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

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

export function utf8ToBytes(str: string): Uint8Array {
  if (typeof TextEncoder !== "undefined") {
    return new TextEncoder().encode(str);
  }
  const out: number[] = [];
  for (let i = 0; i < str.length; i++) {
    let cp = str.charCodeAt(i);
    if (cp >= 0xd800 && cp <= 0xdbff && i + 1 < str.length) {
      const lo = str.charCodeAt(++i);
      cp = 0x10000 + (((cp & 0x3ff) << 10) | (lo & 0x3ff));
    }
    if (cp < 0x80) {
      out.push(cp);
    } else if (cp < 0x800) {
      out.push(0xc0 | (cp >> 6), 0x80 | (cp & 0x3f));
    } else if (cp < 0x10000) {
      out.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
    } else {
      out.push(
        0xf0 | (cp >> 18),
        0x80 | ((cp >> 12) & 0x3f),
        0x80 | ((cp >> 6) & 0x3f),
        0x80 | (cp & 0x3f)
      );
    }
  }
  return new Uint8Array(out);
}

export function bytesToBase64(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const a = bytes[i];
    const b = i + 1 < bytes.length ? bytes[i + 1] : 0;
    const c = i + 2 < bytes.length ? bytes[i + 2] : 0;
    const triple = (a << 16) | (b << 8) | c;
    out += BASE64_CHARS[(triple >> 18) & 63];
    out += BASE64_CHARS[(triple >> 12) & 63];
    out += i + 1 < bytes.length ? BASE64_CHARS[(triple >> 6) & 63] : "=";
    out += i + 2 < bytes.length ? BASE64_CHARS[triple & 63] : "=";
  }
  return out;
}

export function base64ToBytes(b64: string): Uint8Array {
  const clean = b64.replace(/[\r\n\s=]+/g, "");
  const out = new Uint8Array(Math.floor((clean.length * 3) / 4));
  let pos = 0;
  for (let i = 0; i < clean.length; i += 4) {
    const v0 = BASE64_CHARS.indexOf(clean[i]);
    const v1 = BASE64_CHARS.indexOf(clean[i + 1]);
    const v2 = i + 2 < clean.length ? BASE64_CHARS.indexOf(clean[i + 2]) : 0;
    const v3 = i + 3 < clean.length ? BASE64_CHARS.indexOf(clean[i + 3]) : 0;
    const triple = (v0 << 18) | (v1 << 12) | (v2 << 6) | v3;
    out[pos++] = (triple >> 16) & 0xff;
    if (i + 2 < clean.length && pos < out.length) out[pos++] = (triple >> 8) & 0xff;
    if (i + 3 < clean.length && pos < out.length) out[pos++] = triple & 0xff;
  }
  return out;
}

export function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

let fallbackEntropyCounter = 0;

export function randomBytes(len: number): Uint8Array {
  const out = new Uint8Array(len);
  const gCrypto = typeof globalThis !== "undefined" ? (globalThis as { crypto?: Crypto }).crypto : undefined;
  if (gCrypto && typeof gCrypto.getRandomValues === "function") {
    return gCrypto.getRandomValues(out);
  }
  let offset = 0;
  while (offset < len) {
    const perf = typeof performance !== "undefined" && typeof performance.now === "function" ? performance.now() : 0;
    const seedStr = `${Date.now()}:${perf}:${++fallbackEntropyCounter}:${Math.random()}:${Math.random()}:${Math.random()}`;
    const seedBytes = utf8ToBytes(seedStr);
    const mix = new Uint8Array(Math.max(64, seedBytes.length));
    for (let i = 0; i < mix.length; i++) {
      mix[i] = ((seedBytes[i % seedBytes.length] ?? 0) ^ Math.floor(Math.random() * 256)) & 0xff;
    }
    const block = sha512(mix);
    const take = Math.min(block.length, len - offset);
    out.set(block.subarray(0, take), offset);
    offset += take;
  }
  return out;
}

export function randomUUID(): string {
  const gCrypto = typeof globalThis !== "undefined" ? (globalThis as { crypto?: Crypto }).crypto : undefined;
  if (gCrypto && typeof gCrypto.randomUUID === "function") {
    return gCrypto.randomUUID();
  }
  const b = randomBytes(16);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = bytesToHex(b);
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export function deviceIdFromPublicKey(pub: Uint8Array): string {
  return bytesToHex(sha256(pub).slice(0, 8));
}

export async function loadOrCreateClientKeyPair(storage: KeyValueStorage): Promise<ClientKeyPair> {
  const existing = await storage.getItem(KEYPAIR_STORAGE_KEY);
  if (existing) {
    try {
      const parsed = JSON.parse(existing) as { priv?: string; pub?: string };
      if (parsed.priv && parsed.pub) {
        const privateKey = base64ToBytes(parsed.priv);
        const publicKey = base64ToBytes(parsed.pub);
        if (privateKey.length === 32 && publicKey.length === 32) {
          return {
            deviceId: deviceIdFromPublicKey(publicKey),
            privateKey,
            publicKey,
            publicKeyBase64: parsed.pub,
          };
        }
      }
    } catch {
      // Regenerate corrupted keypair entry
    }
  }
  const privateKey = randomBytes(32);
  const publicKey = ed.getPublicKey(privateKey);
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
  const sig = ed.sign(utf8ToBytes(message), privateKey);
  return bytesToBase64(sig);
}

export async function verifyUtf8(publicKeyBase64: string, message: string, sigBase64: string): Promise<boolean> {
  try {
    return ed.verify(
      base64ToBytes(sigBase64),
      utf8ToBytes(message),
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
