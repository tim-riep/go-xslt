// Run results live outside the reducer in store.tsx, keyed by tab id, for
// the same reason buffers do: a result update must re-render only the one
// result panel it belongs to. This also means switching tabs never loses a
// result — it's simply not subscribed to while its tab isn't visible — and
// a result is dropped exactly when its tab closes (see TAB_CLOSE call
// sites, which must also call results.clear(tabId)).
//
// The `seq` field is what makes overlapping runs safe: starting a new run
// bumps it, and a late-arriving response whose seq no longer matches the
// current one is a stale response from a superseded run and is dropped
// rather than clobbering a newer result on screen.
import { useCallback, useSyncExternalStore } from "react";

export type RunStatus = "idle" | "running" | "done" | "error";

export interface RunResultEntry<T = unknown> {
  status: RunStatus;
  result: T | null;
  error: string | null;
  seq: number;
  durationMs: number | null;
}

const EMPTY_ENTRY: RunResultEntry<never> = { status: "idle", result: null, error: null, seq: 0, durationMs: null };

type Listener = () => void;

class ResultsStore {
  private entries = new Map<string, RunResultEntry>();
  private listeners = new Map<string, Set<Listener>>();

  get<T>(tabId: string): RunResultEntry<T> {
    return (this.entries.get(tabId) as RunResultEntry<T> | undefined) ?? (EMPTY_ENTRY as RunResultEntry<T>);
  }

  /** Marks tabId as running and returns the seq this run must present back
   * to finish()/fail() — a mismatch there means a newer run has since
   * started and this response should be dropped. */
  start(tabId: string): number {
    const cur = this.entries.get(tabId);
    const seq = (cur?.seq ?? 0) + 1;
    this.entries.set(tabId, { status: "running", result: cur?.result ?? null, error: null, seq, durationMs: null });
    this.emit(tabId);
    return seq;
  }

  finish<T>(tabId: string, seq: number, result: T, durationMs: number): void {
    const cur = this.entries.get(tabId);
    if (!cur || cur.seq !== seq) return; // superseded by a newer run
    this.entries.set(tabId, { status: "done", result, error: null, seq, durationMs });
    this.emit(tabId);
  }

  fail(tabId: string, seq: number, error: string): void {
    const cur = this.entries.get(tabId);
    if (!cur || cur.seq !== seq) return;
    this.entries.set(tabId, { status: "error", result: null, error, seq, durationMs: null });
    this.emit(tabId);
  }

  clear(tabId: string): void {
    this.entries.delete(tabId);
    this.emit(tabId);
  }

  subscribe(tabId: string, listener: Listener): () => void {
    let set = this.listeners.get(tabId);
    if (!set) {
      set = new Set();
      this.listeners.set(tabId, set);
    }
    set.add(listener);
    return () => {
      set?.delete(listener);
      if (set && set.size === 0) this.listeners.delete(tabId);
    };
  }

  private emit(tabId: string): void {
    this.listeners.get(tabId)?.forEach((l) => l());
  }
}

export const results = new ResultsStore();

export function useResult<T>(tabId: string | null): RunResultEntry<T> {
  const subscribe = useCallback(
    (onStoreChange: () => void) => (tabId ? results.subscribe(tabId, onStoreChange) : () => {}),
    [tabId],
  );
  const getSnapshot = useCallback(() => (tabId ? results.get<T>(tabId) : (EMPTY_ENTRY as RunResultEntry<T>)), [tabId]);
  return useSyncExternalStore(subscribe, getSnapshot);
}
