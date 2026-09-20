// File buffer text lives OUTSIDE the reducer in store.tsx on purpose: a
// keystroke in one open editor must not re-render the tree, the tab strip,
// or any other open editor. This is a plain external store (subscribed via
// useSyncExternalStore, React's own escape hatch for exactly this) keyed by
// "projectId::path" — and since a ProjectState's id IS its directory, that
// key is also literally the pair WriteFile needs, with no extra bookkeeping.
//
// Autosave is debounced per key. The one hard invariant every run call site
// must honor: flushProject(projectId) before running anything, because the
// engine resolves xsl:include/xsl:import/document() from the files on disk —
// an unflushed edit to an included module would silently run stale content.
import { useCallback, useSyncExternalStore } from "react";
import { readFile, writeFile } from "../lib/api";

export interface BufferEntry {
  text: string;
  version: number; // bumped on every setText
  savedVersion: number; // the version last successfully written to disk
  saving: boolean;
  error?: string;
}

const EMPTY_ENTRY: BufferEntry = { text: "", version: 0, savedVersion: 0, saving: false };
const AUTOSAVE_DEBOUNCE_MS = 600;

function keyOf(projectId: string, path: string): string {
  return `${projectId}::${path}`;
}

type Listener = () => void;

class BufferStore {
  private entries = new Map<string, BufferEntry>();
  private listeners = new Map<string, Set<Listener>>();
  private timers = new Map<string, number>();
  private pendingFlush = new Map<string, Promise<void>>();

  /** Loads a file's on-disk content into a fresh, clean (non-dirty) buffer —
   * called once when a file tab is opened. Overwrites any existing entry, so
   * never call this on an already-open, possibly-dirty buffer. */
  hydrate(projectId: string, path: string, text: string): void {
    const key = keyOf(projectId, path);
    this.entries.set(key, { text, version: 0, savedVersion: 0, saving: false });
    this.emit(key);
  }

  getByKey(key: string): BufferEntry {
    return this.entries.get(key) ?? EMPTY_ENTRY;
  }

  /** Loads a path's content into a buffer if (and only if) it isn't already
   * tracked — the shared "open this file for editing, wherever it's being
   * referenced from" primitive used both by opening a plain file tab and by
   * a run view's inline editor for its picked stylesheet/source/instance. */
  async ensureLoaded(projectId: string, path: string): Promise<void> {
    const key = keyOf(projectId, path);
    if (this.entries.has(key)) return;
    const text = await readFile(projectId, path);
    // A concurrent ensureLoaded for the same key (e.g. the file tab and a
    // run view both reference it) may have hydrated it while we awaited —
    // never clobber newer state with this stale read.
    if (!this.entries.has(key)) this.hydrate(projectId, path, text ?? "");
  }

  get(projectId: string, path: string): BufferEntry {
    return this.getByKey(keyOf(projectId, path));
  }

  isDirty(projectId: string, path: string): boolean {
    const e = this.get(projectId, path);
    return e.version !== e.savedVersion;
  }

  /** Updates the in-memory text immediately and schedules a debounced
   * autosave. The editor is always driven by this value, never by what's on
   * disk, until the user's own edits are what's on disk. */
  setText(projectId: string, path: string, text: string): void {
    const key = keyOf(projectId, path);
    const cur = this.getByKey(key);
    this.entries.set(key, { ...cur, text, version: cur.version + 1 });
    this.emit(key);

    window.clearTimeout(this.timers.get(key));
    this.timers.set(
      key,
      window.setTimeout(() => {
        void this.flush(projectId, path);
      }, AUTOSAVE_DEBOUNCE_MS),
    );
  }

  /** Writes the current text to disk if dirty, coalescing concurrent callers
   * (an explicit flush racing the debounce timer) onto one in-flight write. */
  flush(projectId: string, path: string): Promise<void> {
    const key = keyOf(projectId, path);
    const inFlight = this.pendingFlush.get(key);
    if (inFlight) return inFlight;

    window.clearTimeout(this.timers.get(key));
    this.timers.delete(key);

    const entry = this.entries.get(key);
    if (!entry || entry.version === entry.savedVersion) return Promise.resolve();

    const versionToSave = entry.version;
    const textToSave = entry.text;
    this.entries.set(key, { ...entry, saving: true });
    this.emit(key);

    const p = writeFile(projectId, path, textToSave)
      .then(() => {
        const latest = this.entries.get(key);
        if (latest) {
          this.entries.set(key, {
            ...latest,
            saving: false,
            savedVersion: Math.max(latest.savedVersion, versionToSave),
            error: undefined,
          });
          this.emit(key);
        }
      })
      .catch((e: unknown) => {
        const latest = this.entries.get(key);
        if (latest) {
          this.entries.set(key, { ...latest, saving: false, error: String(e) });
          this.emit(key);
        }
      })
      .finally(() => {
        this.pendingFlush.delete(key);
      });
    this.pendingFlush.set(key, p);
    return p;
  }

  /** Flushes every dirty buffer belonging to projectId — call before every
   * run so cross-file references never resolve stale content. */
  flushProject(projectId: string): Promise<void> {
    const prefix = `${projectId}::`;
    const work: Promise<void>[] = [];
    for (const key of this.entries.keys()) {
      if (!key.startsWith(prefix)) continue;
      const path = key.slice(prefix.length);
      work.push(this.flush(projectId, path));
    }
    return Promise.all(work).then(() => undefined);
  }

  /** Moves a buffer to a new path after a rename/move — must be called by
   * the same code path that dispatches PATH_RENAMED to the app reducer, so
   * the two stores never disagree about where a file's content lives. */
  rename(projectId: string, fromPath: string, toPath: string): void {
    const oldKey = keyOf(projectId, fromPath);
    const newKey = keyOf(projectId, toPath);
    const entry = this.entries.get(oldKey);
    if (!entry) return;
    this.entries.delete(oldKey);
    this.entries.set(newKey, entry);
    const timer = this.timers.get(oldKey);
    if (timer !== undefined) {
      this.timers.delete(oldKey);
      this.timers.set(newKey, timer);
    }
    this.emit(oldKey);
    this.emit(newKey);
  }

  /** Like rename(), but for a folder move/rename: remaps every buffer whose
   * path is fromPath itself OR nested under it (fromPath's own children keep
   * their relative shape under toPath). Exact-path rename() alone only
   * handles a single file. */
  renamePrefix(projectId: string, fromPath: string, toPath: string): void {
    const prefix = `${projectId}::`;
    const affected: string[] = [];
    for (const key of this.entries.keys()) {
      if (!key.startsWith(prefix)) continue;
      const path = key.slice(prefix.length);
      if (path === fromPath || path.startsWith(`${fromPath}/`)) affected.push(path);
    }
    for (const path of affected) {
      const newPath = path === fromPath ? toPath : toPath + path.slice(fromPath.length);
      this.rename(projectId, path, newPath);
    }
  }

  /** Discards every buffer under path (path itself or any descendant) —
   * called after deleting a file or folder so no orphaned buffer lingers. */
  discardPrefix(projectId: string, path: string): void {
    const prefix = `${projectId}::`;
    for (const key of Array.from(this.entries.keys())) {
      if (!key.startsWith(prefix)) continue;
      const p = key.slice(prefix.length);
      if (p === path || p.startsWith(`${path}/`)) this.discard(projectId, p);
    }
  }

  /** Drops a buffer entirely (tab closed on a clean file, or the file was
   * deleted) — discards any pending debounce, but does NOT flush first;
   * callers that need the pending edit saved must flush before discarding. */
  discard(projectId: string, path: string): void {
    const key = keyOf(projectId, path);
    window.clearTimeout(this.timers.get(key));
    this.timers.delete(key);
    this.entries.delete(key);
    this.emit(key);
  }

  subscribeKey(key: string, listener: Listener): () => void {
    let set = this.listeners.get(key);
    if (!set) {
      set = new Set();
      this.listeners.set(key, set);
    }
    set.add(listener);
    return () => {
      set?.delete(listener);
      if (set && set.size === 0) this.listeners.delete(key);
    };
  }

  private emit(key: string): void {
    this.listeners.get(key)?.forEach((l) => l());
  }
}

export const buffers = new BufferStore();

/** Subscribes to one file's buffer entry. Pass null for either argument
 * while no file is selected — returns the stable EMPTY_ENTRY, never a fresh
 * object, so it never causes a spurious re-render. */
export function useBuffer(projectId: string | null, path: string | null): BufferEntry {
  const key = projectId && path != null ? keyOf(projectId, path) : null;
  const subscribe = useCallback(
    (onStoreChange: () => void) => (key ? buffers.subscribeKey(key, onStoreChange) : () => {}),
    [key],
  );
  const getSnapshot = useCallback(() => (key ? buffers.getByKey(key) : EMPTY_ENTRY), [key]);
  return useSyncExternalStore(subscribe, getSnapshot);
}
