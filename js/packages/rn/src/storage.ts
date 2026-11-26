import type { KeyValueStorage } from "@corekiosk/client";

export interface SecureStoreLike {
  getItemAsync(key: string): Promise<string | null>;
  setItemAsync(key: string, value: string): Promise<void>;
  deleteItemAsync(key: string): Promise<void>;
}

/**
 * Browser localStorage-backed KeyValueStorage for React Native Web, with automatic
 * in-memory fallback when localStorage is unavailable (e.g. strict privacy mode / SSR).
 */
export class WebLocalStorage implements KeyValueStorage {
  private readonly mem = new Map<string, string>();

  private get store(): Storage | null {
    try {
      return typeof globalThis !== "undefined" && "localStorage" in globalThis
        ? (globalThis as unknown as { localStorage: Storage }).localStorage
        : null;
    } catch {
      return null;
    }
  }

  async getItem(key: string): Promise<string | null> {
    const s = this.store;
    if (s) {
      try {
        return s.getItem(key);
      } catch {}
    }
    return this.mem.get(key) ?? null;
  }

  async setItem(key: string, value: string): Promise<void> {
    const s = this.store;
    if (s) {
      try {
        s.setItem(key, value);
        return;
      } catch {}
    }
    this.mem.set(key, value);
  }

  async removeItem(key: string): Promise<void> {
    const s = this.store;
    if (s) {
      try {
        s.removeItem(key);
        return;
      } catch {}
    }
    this.mem.delete(key);
  }
}

/**
 * Adapts expo-secure-store to @corekiosk/client's KeyValueStorage interface, falling back
 * to WebLocalStorage automatically on platforms where SecureStore is unsupported (Web).
 */
export class ExpoSecureStorage implements KeyValueStorage {
  private readonly fallback = new WebLocalStorage();

  constructor(private readonly secureStore: SecureStoreLike) {}

  private sanitize(key: string): string {
    return key.replace(/[^A-Za-z0-9._-]/g, "_");
  }

  async getItem(key: string): Promise<string | null> {
    try {
      return await this.secureStore.getItemAsync(this.sanitize(key));
    } catch {
      return this.fallback.getItem(key);
    }
  }

  async setItem(key: string, value: string): Promise<void> {
    try {
      await this.secureStore.setItemAsync(this.sanitize(key), value);
    } catch {
      await this.fallback.setItem(key, value);
    }
  }

  async removeItem(key: string): Promise<void> {
    try {
      await this.secureStore.deleteItemAsync(this.sanitize(key));
    } catch {
      await this.fallback.removeItem(key);
    }
  }
}
