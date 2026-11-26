import type {
  BoardView,
  CatalogView,
  RulesetView,
  SchemaSnapshot,
  SettingSpec,
  Snapshot,
} from "./generated/types.js";

export interface KioskState {
  connection: "disconnected" | "connecting" | "connected";
  snapshot: Snapshot | null;
  schema: SchemaSnapshot | null;
  configRevision: number;
  values: Record<string, unknown>;
  runtimeValues: Record<string, unknown>;
  settings: SettingSpec[];
  boards: Record<string, BoardView>;
  catalogs: Record<string, CatalogView>;
  rulesets: Record<string, RulesetView>;
  clockOffsetMs: number;
  rttMs: number;
  operatorUsername: string | null;
}

type Listener = (state: KioskState) => void;

export class KioskStore {
  private state: KioskState = {
    connection: "disconnected",
    snapshot: null,
    schema: null,
    configRevision: 0,
    values: {},
    runtimeValues: {},
    settings: [],
    boards: {},
    catalogs: {},
    rulesets: {},
    clockOffsetMs: 0,
    rttMs: 0,
    operatorUsername: null,
  };
  private listeners = new Set<Listener>();

  getState(): KioskState {
    return this.state;
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    fn(this.state);
    return () => this.listeners.delete(fn);
  }

  patch(partial: Partial<KioskState>): void {
    this.state = { ...this.state, ...partial };
    for (const fn of this.listeners) fn(this.state);
  }

  serverNowMs(): number {
    return Date.now() + this.state.clockOffsetMs;
  }
}
