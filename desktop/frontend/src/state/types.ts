import type { ProjectManifest, SavedRun } from "../lib/api";
import type { FlatNode, TabId } from "./paths";

export interface ProjectState {
  id: string; // == dir, the stable identity of a project
  dir: string;
  name: string;
  manifest: ProjectManifest;
  nodes: Record<string, FlatNode>;
  rootChildren: string[];
  expanded: Record<string, true>;
  scanning: boolean;
  error?: string;
}

export interface FileTab {
  id: TabId;
  kind: "file";
  projectId: string;
  path: string;
  pinned: boolean;
  missing: boolean;
}

export interface RunTab {
  id: TabId;
  kind: "run";
  projectId: string;
  runId: string;
  pinned: boolean;
}

export type Tab = FileTab | RunTab;

// ---- transient UI overlays (dialogs / menus / toasts) ----
// Callback-bearing state is unusual for a reducer, but it keeps Modal/
// ContextMenu fully generic: they render whatever the dispatching component
// asked for and report back through the closure, with no dedicated action
// type per call site.

export type DialogState =
  | {
      kind: "prompt";
      title: string;
      label?: string;
      initialValue?: string;
      confirmLabel?: string;
      placeholder?: string;
      validate?: (value: string) => string | null;
      onConfirm: (value: string) => void;
      onCancel?: () => void;
    }
  | {
      kind: "confirm";
      title: string;
      message: string;
      confirmLabel?: string;
      danger?: boolean;
      onConfirm: () => void;
      onCancel?: () => void;
    };

export interface ContextMenuItem {
  id: string;
  label: string;
  danger?: boolean;
  disabled?: boolean;
  separatorBefore?: boolean;
  onSelect: () => void;
}

export interface ContextMenuState {
  x: number;
  y: number;
  items: ContextMenuItem[];
}

export interface Toast {
  id: string;
  kind: "info" | "error" | "success";
  message: string;
}

export interface AppState {
  knownProjects: import("../lib/api").ProjectRef[];
  projects: Record<string, ProjectState>;
  projectOrder: string[];
  activeProjectId: string | null;

  tabs: Record<TabId, Tab>;
  tabOrder: TabId[];
  activeTabId: TabId | null;
  previewTabId: TabId | null;

  ui: {
    sidebarSection: "files" | "runs";
    dialog: DialogState | null;
    menu: ContextMenuState | null;
    toasts: Toast[];
  };
}

export const initialAppState: AppState = {
  knownProjects: [],
  projects: {},
  projectOrder: [],
  activeProjectId: null,

  tabs: {},
  tabOrder: [],
  activeTabId: null,
  previewTabId: null,

  ui: {
    sidebarSection: "files",
    dialog: null,
    menu: null,
    toasts: [],
  },
};

export type { SavedRun };
