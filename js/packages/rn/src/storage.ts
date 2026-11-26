import type { KeyValueStorage } from "@corekiosk/client";

export interface SecureStoreLike {
  getItemAsync(key: string): Promise<string | null>;
  setItemAsync(key: string, value: string): Promise<void>;
  deleteItemAsync(key: string): Promise<void>;
}

/**
 * Adapts expo-secure-store to @corekiosk/client's KeyValueStorage interface.
 * Keys are sanitized to match SecureStore's allowed character set ([A-Za-z0-9._-]).
 */
export class ExpoSecureStorage implements KeyValueStorage {
  constructor(private readonly secureStore: SecureStoreLike) {}

  private sanitize(key: string): string {
    return key.replace(/[^A-Za-z0-9._-]/g, "_");
  }

  getItem(key: string): Promise<string | null> {
    return this.secureStore.getItemAsync(this.sanitize(key));
  }

  setItem(key: string, value: string): Promise<void> {
    return this.secureStore.setItemAsync(this.sanitize(key), value);
  }

  removeItem(key: string): Promise<void> {
    return this.secureStore.deleteItemAsync(this.sanitize(key));
  }
}
