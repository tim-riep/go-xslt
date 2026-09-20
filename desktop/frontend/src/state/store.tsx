// Structural application state: projects, the scanned file tree, tabs, and
// transient UI overlays (dialogs/menus/toasts). Deliberately NOT here: file
// buffer text (see buffers.ts) and run results (see results.ts) — putting
// either in this reducer would re-render every open CodeMirror instance and
// every result panel on every keystroke / tick.
import { createContext, useContext, useMemo, useReducer, type ReactNode } from "react";
import type { FileNode, Project, ProjectRef, SavedRun } from "../lib/api";
import { flattenTree, isSelfOrDescendant, remapPath, type TabId } from "./paths";
import { initialAppState, type AppState, type ContextMenuState, type DialogState, type Tab, type Toast } from "./types";

export type Action =
  | { type: "KNOWN_PROJECTS_LOADED"; refs: ProjectRef[] }
  | { type: "PROJECT_OPENED"; project: Project }
  | { type: "PROJECT_CLOSED"; projectId: string }
  | { type: "PROJECT_ACTIVATED"; projectId: string }
  | { type: "TREE_SCAN_START"; projectId: string }
  | { type: "TREE_SCANNED"; projectId: string; tree: FileNode }
  | { type: "NODE_TOGGLED"; projectId: string; path: string }
  | { type: "PATH_RENAMED"; projectId: string; from: string; to: string }
  | { type: "PATH_DELETED"; projectId: string; path: string }
  | { type: "RUN_UPSERTED"; projectId: string; run: SavedRun }
  | { type: "RUN_DELETED"; projectId: string; runId: string }
  | { type: "TAB_OPEN"; tab: Tab; preview?: boolean }
  | { type: "TAB_PROMOTE"; id: TabId }
  | { type: "TAB_CLOSE"; id: TabId }
  | { type: "TAB_CLOSE_OTHERS"; id: TabId }
  | { type: "TAB_CLOSE_ALL" }
  | { type: "TAB_ACTIVATE"; id: TabId }
  | { type: "TAB_PIN"; id: TabId; pinned: boolean }
  | { type: "TAB_MOVE"; id: TabId; toIndex: number }
  | { type: "SIDEBAR_SECTION"; section: "files" | "runs" }
  | { type: "DIALOG_OPEN"; dialog: DialogState }
  | { type: "DIALOG_CLOSE" }
  | { type: "MENU_OPEN"; menu: ContextMenuState }
  | { type: "MENU_CLOSE" }
  | { type: "TOAST_PUSH"; toast: Toast }
  | { type: "TOAST_DISMISS"; id: string };

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  if (!(key in obj)) return obj;
  const next = { ...obj };
  delete (next as Record<string, unknown>)[key];
  return next;
}

/** Picks which tab becomes active after `removedId` is closed: the tab right
 * after it in tabOrder, else the one before, else none. */
function nextActiveAfterClose(tabOrder: TabId[], removedId: TabId): TabId | null {
  const i = tabOrder.indexOf(removedId);
  if (i < 0) return null;
  const rest = tabOrder.filter((id) => id !== removedId);
  if (rest.length === 0) return null;
  return rest[Math.min(i, rest.length - 1)];
}

function remapRunPaths(run: SavedRun, from: string, to: string): SavedRun {
  const next: SavedRun = { ...run };
  let changed = false;
  if (next.stylesheet && isSelfOrDescendant(next.stylesheet, from)) {
    next.stylesheet = remapPath(next.stylesheet, from, to);
    changed = true;
  }
  if (next.source && isSelfOrDescendant(next.source, from)) {
    next.source = remapPath(next.source, from, to);
    changed = true;
  }
  if (next.instance && isSelfOrDescendant(next.instance, from)) {
    next.instance = remapPath(next.instance, from, to);
    changed = true;
  }
  if (next.contextDoc && isSelfOrDescendant(next.contextDoc, from)) {
    next.contextDoc = remapPath(next.contextDoc, from, to);
    changed = true;
  }
  if (next.schemas?.some((s) => isSelfOrDescendant(s, from))) {
    next.schemas = next.schemas.map((s) => (isSelfOrDescendant(s, from) ? remapPath(s, from, to) : s));
    changed = true;
  }
  return changed ? next : run;
}

function closeTabsForProject(state: AppState, projectId: string): Pick<AppState, "tabs" | "tabOrder" | "activeTabId" | "previewTabId"> {
  const keepIds = state.tabOrder.filter((id) => state.tabs[id]?.projectId !== projectId);
  const tabs: AppState["tabs"] = {};
  for (const id of keepIds) tabs[id] = state.tabs[id];
  return {
    tabs,
    tabOrder: keepIds,
    activeTabId: state.activeTabId && tabs[state.activeTabId] ? state.activeTabId : keepIds[0] ?? null,
    previewTabId: state.previewTabId && tabs[state.previewTabId] ? state.previewTabId : null,
  };
}

function reducer(state: AppState, action: Action): AppState {
  switch (action.type) {
    case "KNOWN_PROJECTS_LOADED":
      return { ...state, knownProjects: action.refs };

    case "PROJECT_OPENED": {
      const { project } = action;
      const { nodes, rootChildren } = project.tree ? flattenTree(project.tree) : { nodes: {}, rootChildren: [] };
      const expanded: Record<string, true> = {};
      for (const p of project.manifest.ui?.expanded ?? []) expanded[p] = true;
      const id = project.dir;
      return {
        ...state,
        projects: {
          ...state.projects,
          [id]: {
            id,
            dir: project.dir,
            name: project.manifest.name,
            manifest: project.manifest,
            nodes,
            rootChildren,
            expanded,
            scanning: false,
          },
        },
        projectOrder: state.projectOrder.includes(id) ? state.projectOrder : [...state.projectOrder, id],
        activeProjectId: id,
      };
    }

    case "PROJECT_CLOSED": {
      if (!state.projects[action.projectId]) return state;
      const rest = closeTabsForProject(state, action.projectId);
      const projects = omit(state.projects, action.projectId);
      const projectOrder = state.projectOrder.filter((id) => id !== action.projectId);
      return {
        ...state,
        ...rest,
        projects,
        projectOrder,
        activeProjectId: state.activeProjectId === action.projectId ? projectOrder[0] ?? null : state.activeProjectId,
      };
    }

    case "PROJECT_ACTIVATED":
      return state.projects[action.projectId] ? { ...state, activeProjectId: action.projectId } : state;

    case "TREE_SCAN_START": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      return { ...state, projects: { ...state.projects, [p.id]: { ...p, scanning: true } } };
    }

    case "TREE_SCANNED": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      const { nodes, rootChildren } = flattenTree(action.tree);
      // Reconcile "missing" on every open file tab of this project against
      // the fresh tree — catches externally-restored/removed files, not just
      // ones this app deleted itself (that path is handled by PATH_DELETED
      // for immediate feedback, ahead of the next rescan).
      let tabs = state.tabs;
      let changed = false;
      for (const id of state.tabOrder) {
        const t = tabs[id];
        if (t?.kind !== "file" || t.projectId !== action.projectId) continue;
        const missing = !(t.path in nodes);
        if (missing !== t.missing) {
          if (!changed) {
            tabs = { ...tabs };
            changed = true;
          }
          tabs[id] = { ...t, missing };
        }
      }
      return {
        ...state,
        projects: { ...state.projects, [p.id]: { ...p, nodes, rootChildren, scanning: false } },
        tabs,
      };
    }

    case "NODE_TOGGLED": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      const expanded = { ...p.expanded };
      if (expanded[action.path]) delete expanded[action.path];
      else expanded[action.path] = true;
      return { ...state, projects: { ...state.projects, [p.id]: { ...p, expanded } } };
    }

    case "PATH_RENAMED": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      const { from, to } = action;

      const runs = p.manifest.runs?.map((r) => remapRunPaths(r, from, to)) ?? p.manifest.runs;
      const project = { ...p, manifest: { ...p.manifest, runs } };

      let tabs = state.tabs;
      let tabOrder = state.tabOrder;
      let activeTabId = state.activeTabId;
      let previewTabId = state.previewTabId;
      for (const id of state.tabOrder) {
        const t = state.tabs[id];
        if (t?.kind !== "file" || t.projectId !== action.projectId || !isSelfOrDescendant(t.path, from)) continue;
        const newPath = remapPath(t.path, from, to);
        const newId = `file:${action.projectId}:${newPath}`;
        if (tabs === state.tabs) tabs = { ...state.tabs };
        tabs = omit(tabs, id);
        tabs[newId] = { ...t, id: newId, path: newPath };
        tabOrder = tabOrder.map((tid) => (tid === id ? newId : tid));
        if (activeTabId === id) activeTabId = newId;
        if (previewTabId === id) previewTabId = newId;
      }

      return {
        ...state,
        projects: { ...state.projects, [p.id]: project },
        tabs,
        tabOrder,
        activeTabId,
        previewTabId,
      };
    }

    case "PATH_DELETED": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      let tabs = state.tabs;
      for (const id of state.tabOrder) {
        const t = state.tabs[id];
        if (t?.kind !== "file" || t.projectId !== action.projectId || !isSelfOrDescendant(t.path, action.path)) continue;
        if (tabs === state.tabs) tabs = { ...state.tabs };
        tabs[id] = { ...t, missing: true };
      }
      return { ...state, tabs };
    }

    case "RUN_UPSERTED": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      const runs = p.manifest.runs ?? [];
      const i = runs.findIndex((r) => r.id === action.run.id);
      const nextRuns = i >= 0 ? runs.map((r, idx) => (idx === i ? action.run : r)) : [...runs, action.run];
      return {
        ...state,
        projects: { ...state.projects, [p.id]: { ...p, manifest: { ...p.manifest, runs: nextRuns } } },
      };
    }

    case "RUN_DELETED": {
      const p = state.projects[action.projectId];
      if (!p) return state;
      const runs = (p.manifest.runs ?? []).filter((r) => r.id !== action.runId);
      const keepIds = state.tabOrder.filter(
        (id) => !(state.tabs[id]?.kind === "run" && state.tabs[id].projectId === action.projectId && (state.tabs[id] as { runId?: string }).runId === action.runId),
      );
      const tabs: AppState["tabs"] = {};
      for (const id of keepIds) tabs[id] = state.tabs[id];
      return {
        ...state,
        projects: { ...state.projects, [p.id]: { ...p, manifest: { ...p.manifest, runs } } },
        tabs,
        tabOrder: keepIds,
        activeTabId: state.activeTabId && tabs[state.activeTabId] ? state.activeTabId : keepIds[0] ?? null,
        previewTabId: state.previewTabId && tabs[state.previewTabId] ? state.previewTabId : null,
      };
    }

    case "TAB_OPEN": {
      const { tab, preview = false } = action;
      if (state.tabs[tab.id]) {
        return {
          ...state,
          activeTabId: tab.id,
          previewTabId: !preview && state.previewTabId === tab.id ? null : state.previewTabId,
        };
      }

      let tabOrder = state.tabOrder;
      let tabs = state.tabs;
      let previewTabId = state.previewTabId;

      if (preview && previewTabId) {
        const i = tabOrder.indexOf(previewTabId);
        const withoutOld = tabOrder.filter((id) => id !== previewTabId);
        const insertAt = i >= 0 ? i : withoutOld.length;
        tabOrder = [...withoutOld.slice(0, insertAt), tab.id, ...withoutOld.slice(insertAt)];
        tabs = omit(tabs, previewTabId);
      } else {
        const insertAt = state.activeTabId ? tabOrder.indexOf(state.activeTabId) + 1 : tabOrder.length;
        tabOrder = [...tabOrder.slice(0, insertAt), tab.id, ...tabOrder.slice(insertAt)];
      }

      return {
        ...state,
        tabs: { ...tabs, [tab.id]: tab },
        tabOrder,
        activeTabId: tab.id,
        previewTabId: preview ? tab.id : previewTabId,
      };
    }

    case "TAB_PROMOTE":
      return state.previewTabId === action.id ? { ...state, previewTabId: null } : state;

    case "TAB_CLOSE": {
      if (!state.tabs[action.id]) return state;
      const tabOrder = state.tabOrder.filter((id) => id !== action.id);
      const nextActive = state.activeTabId === action.id ? nextActiveAfterClose(state.tabOrder, action.id) : state.activeTabId;
      return {
        ...state,
        tabs: omit(state.tabs, action.id),
        tabOrder,
        activeTabId: nextActive,
        previewTabId: state.previewTabId === action.id ? null : state.previewTabId,
      };
    }

    case "TAB_CLOSE_OTHERS": {
      const keepIds = state.tabOrder.filter((id) => id === action.id || state.tabs[id]?.pinned);
      const tabs: AppState["tabs"] = {};
      for (const id of keepIds) tabs[id] = state.tabs[id];
      return {
        ...state,
        tabs,
        tabOrder: keepIds,
        activeTabId: action.id,
        previewTabId: state.previewTabId && tabs[state.previewTabId] ? state.previewTabId : null,
      };
    }

    case "TAB_CLOSE_ALL": {
      const keepIds = state.tabOrder.filter((id) => state.tabs[id]?.pinned);
      const tabs: AppState["tabs"] = {};
      for (const id of keepIds) tabs[id] = state.tabs[id];
      return { ...state, tabs, tabOrder: keepIds, activeTabId: keepIds[0] ?? null, previewTabId: null };
    }

    case "TAB_ACTIVATE":
      return state.tabs[action.id] ? { ...state, activeTabId: action.id } : state;

    case "TAB_PIN": {
      const t = state.tabs[action.id];
      if (!t) return state;
      return {
        ...state,
        tabs: { ...state.tabs, [action.id]: { ...t, pinned: action.pinned } },
        previewTabId: action.pinned && state.previewTabId === action.id ? null : state.previewTabId,
      };
    }

    case "TAB_MOVE": {
      if (!state.tabs[action.id]) return state;
      const without = state.tabOrder.filter((id) => id !== action.id);
      const at = Math.max(0, Math.min(action.toIndex, without.length));
      return { ...state, tabOrder: [...without.slice(0, at), action.id, ...without.slice(at)] };
    }

    case "SIDEBAR_SECTION":
      return { ...state, ui: { ...state.ui, sidebarSection: action.section } };

    case "DIALOG_OPEN":
      return { ...state, ui: { ...state.ui, dialog: action.dialog } };
    case "DIALOG_CLOSE":
      return { ...state, ui: { ...state.ui, dialog: null } };

    case "MENU_OPEN":
      return { ...state, ui: { ...state.ui, menu: action.menu } };
    case "MENU_CLOSE":
      return { ...state, ui: { ...state.ui, menu: null } };

    case "TOAST_PUSH":
      return { ...state, ui: { ...state.ui, toasts: [...state.ui.toasts, action.toast].slice(-5) } };
    case "TOAST_DISMISS":
      return { ...state, ui: { ...state.ui, toasts: state.ui.toasts.filter((t) => t.id !== action.id) } };

    default:
      return state;
  }
}

const StateContext = createContext<AppState | null>(null);
const DispatchContext = createContext<((action: Action) => void) | null>(null);

export function AppStateProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(reducer, initialAppState);
  const stableDispatch = useMemo(() => dispatch, []);
  return (
    <StateContext.Provider value={state}>
      <DispatchContext.Provider value={stableDispatch}>{children}</DispatchContext.Provider>
    </StateContext.Provider>
  );
}

export function useAppState(): AppState {
  const ctx = useContext(StateContext);
  if (!ctx) throw new Error("useAppState must be used within AppStateProvider");
  return ctx;
}

export function useAppDispatch(): (action: Action) => void {
  const ctx = useContext(DispatchContext);
  if (!ctx) throw new Error("useAppDispatch must be used within AppStateProvider");
  return ctx;
}

export function useActiveProject() {
  const state = useAppState();
  return state.activeProjectId ? state.projects[state.activeProjectId] ?? null : null;
}

export function useActiveTab(): Tab | null {
  const state = useAppState();
  return state.activeTabId ? state.tabs[state.activeTabId] ?? null : null;
}

export function useOrderedTabs(): Tab[] {
  const state = useAppState();
  return state.tabOrder.map((id) => state.tabs[id]).filter((t): t is Tab => Boolean(t));
}
