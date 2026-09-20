// Orchestration layer: every action here talks to the Go backend (via
// lib/api) and/or coordinates the three stores (structural state, file
// buffers, run results) that a single user gesture can touch. Components
// dispatch plain reducer actions directly for anything that's pure UI state
// (activating/pinning a tab, expanding a tree row); they call into this hook
// for anything that reads or writes a project's files or manifest.
import { useCallback, useRef } from "react";
import {
  createFile,
  createFolder,
  createProject,
  deletePath,
  duplicatePath,
  forgetProject,
  openProject,
  pickFolder,
  renamePath,
  reveal,
  saveManifest,
  listProjects,
  refreshTree,
  type SavedRun,
  type RunKind,
} from "../lib/api";
import { buffers } from "./buffers";
import { results } from "./results";
import { useConfirm, usePrompt, useToast } from "./overlays";
import { useAppDispatch, useAppState } from "./store";
import { fileTabId, join, runTabId } from "./paths";
import type { FileTab, Tab } from "./types";

let runIdSeq = 0;
function genRunId(): string {
  return `run-${Date.now().toString(36)}-${(++runIdSeq).toString(36)}`;
}

const sampleSchema = `<?xml version="1.0" encoding="UTF-8"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="note">
    <xs:complexType>
      <xs:sequence>
        <xs:element name="to" type="xs:string"/>
        <xs:element name="body" type="xs:string"/>
      </xs:sequence>
      <xs:attribute name="priority" type="xs:integer"/>
    </xs:complexType>
  </xs:element>
</xs:schema>
`;

const sampleNote = `<?xml version="1.0" encoding="UTF-8"?>
<note priority="1">
  <to>Tim</to>
  <body>Hello there</body>
</note>
`;

const sampleCatalog = `<?xml version="1.0" encoding="UTF-8"?>
<catalog>
  <book price="42"><title>The Go Programming Language</title></book>
  <book price="28"><title>XSLT 3.0</title></book>
</catalog>
`;

function defaultRun(kind: RunKind): SavedRun {
  const id = genRunId();
  switch (kind) {
    case "transform":
      return { id, name: "Untitled transform", kind, stylesheet: "", source: "", params: {} };
    case "validate":
      // Inline by design (see ValidateRunView) — schemas/instance are typed
      // directly into the run, no project file required, seeded with a tiny
      // sample so the first thing you see isn't a blank editor.
      return { id, name: "Untitled validation", kind, schemas: [], schemaInline: sampleSchema, instance: "", instanceInline: sampleNote, xsdVersion: "1.0" };
    case "xpath":
      return { id, name: "Untitled XPath", kind, expression: "//book[@price > 30]/title/string()", contextDoc: "", contextInline: sampleCatalog };
    default:
      // The generated RunKind enum also carries Go's zero value ("") for
      // JSON round-tripping — never a kind the UI itself asks to create.
      throw new Error(`unknown run kind: ${kind}`);
  }
}

export function useProjectActions() {
  const state = useAppState();
  const dispatch = useAppDispatch();
  const toast = useToast();
  const prompt = usePrompt();
  const confirm = useConfirm();

  const refreshKnownProjects = useCallback(async () => {
    const refs = await listProjects();
    dispatch({ type: "KNOWN_PROJECTS_LOADED", refs: refs ?? [] });
  }, [dispatch]);

  const openDir = useCallback(
    async (dir: string) => {
      try {
        const project = await openProject(dir);
        if (project) dispatch({ type: "PROJECT_OPENED", project });
        await refreshKnownProjects();
      } catch (e) {
        toast("error", `Couldn't open project: ${String(e)}`);
      }
    },
    [dispatch, refreshKnownProjects, toast],
  );

  const createAndOpen = useCallback(
    async (name: string) => {
      try {
        const project = await createProject(name);
        if (project) dispatch({ type: "PROJECT_OPENED", project });
        await refreshKnownProjects();
      } catch (e) {
        toast("error", `Couldn't create project: ${String(e)}`);
      }
    },
    [dispatch, refreshKnownProjects, toast],
  );

  const promptNewProject = useCallback(async () => {
    const name = await prompt({ title: "New project", label: "Name", placeholder: "My project" });
    if (name) await createAndOpen(name);
  }, [prompt, createAndOpen]);

  const openFolderPicker = useCallback(async () => {
    const dir = await pickFolder();
    if (dir) await openDir(dir);
  }, [openDir]);

  const tabsForProject = useCallback(
    (projectId: string) => state.tabOrder.map((id) => state.tabs[id]).filter((t): t is Tab => Boolean(t) && t.projectId === projectId),
    [state.tabOrder, state.tabs],
  );

  const releaseTab = useCallback((projectId: string, tab: Tab) => {
    if (tab.kind === "file") void buffers.flush(projectId, tab.path);
    results.clear(tab.id);
  }, []);

  const closeProject = useCallback(
    (projectId: string) => {
      for (const tab of tabsForProject(projectId)) releaseTab(projectId, tab);
      dispatch({ type: "PROJECT_CLOSED", projectId });
    },
    [tabsForProject, releaseTab, dispatch],
  );

  const forgetKnown = useCallback(
    async (dir: string) => {
      if (state.projects[dir]) closeProject(dir);
      await forgetProject(dir);
      await refreshKnownProjects();
    },
    [state.projects, closeProject, forgetProject, refreshKnownProjects],
  );

  const rescan = useCallback(
    async (projectId: string) => {
      dispatch({ type: "TREE_SCAN_START", projectId });
      try {
        const tree = await refreshTree(projectId);
        if (tree) dispatch({ type: "TREE_SCANNED", projectId, tree });
      } catch (e) {
        toast("error", `Couldn't read project files: ${String(e)}`);
      }
    },
    [dispatch, toast],
  );

  const toggleNode = useCallback((projectId: string, path: string) => dispatch({ type: "NODE_TOGGLED", projectId, path }), [dispatch]);

  const openFile = useCallback(
    async (projectId: string, path: string, opts?: { preview?: boolean }) => {
      try {
        await buffers.ensureLoaded(projectId, path);
      } catch (e) {
        toast("error", `Couldn't open ${path}: ${String(e)}`);
        return;
      }
      const id = fileTabId(projectId, path);
      const tab: FileTab = { id, kind: "file", projectId, path, pinned: false, missing: false };
      dispatch({ type: "TAB_OPEN", tab, preview: opts?.preview });
    },
    [dispatch, toast],
  );

  const openRun = useCallback(
    (projectId: string, runId: string) => {
      dispatch({ type: "TAB_OPEN", tab: { id: runTabId(projectId, runId), kind: "run", projectId, runId, pinned: false } });
    },
    [dispatch],
  );

  const closeTab = useCallback(
    (tab: Tab) => {
      releaseTab(tab.projectId, tab);
      dispatch({ type: "TAB_CLOSE", id: tab.id });
    },
    [releaseTab, dispatch],
  );

  const closeOtherTabs = useCallback(
    (keepId: string) => {
      for (const id of state.tabOrder) {
        const t = state.tabs[id];
        if (t && id !== keepId && !t.pinned) releaseTab(t.projectId, t);
      }
      dispatch({ type: "TAB_CLOSE_OTHERS", id: keepId });
    },
    [state.tabOrder, state.tabs, releaseTab, dispatch],
  );

  const closeAllTabs = useCallback(() => {
    for (const id of state.tabOrder) {
      const t = state.tabs[id];
      if (t && !t.pinned) releaseTab(t.projectId, t);
    }
    dispatch({ type: "TAB_CLOSE_ALL" });
  }, [state.tabOrder, state.tabs, releaseTab, dispatch]);

  const newFile = useCallback(
    async (projectId: string, dir: string, name: string, content = "") => {
      const rel = join(dir, name);
      try {
        await createFile(projectId, rel, content);
        await rescan(projectId);
        await openFile(projectId, rel);
        return rel;
      } catch (e) {
        toast("error", `Couldn't create ${name}: ${String(e)}`);
        return null;
      }
    },
    [rescan, openFile, toast],
  );

  const newFolder = useCallback(
    async (projectId: string, dir: string, name: string) => {
      const rel = join(dir, name);
      try {
        await createFolder(projectId, rel);
        await rescan(projectId);
        dispatch({ type: "NODE_TOGGLED", projectId, path: rel });
        return rel;
      } catch (e) {
        toast("error", `Couldn't create folder ${name}: ${String(e)}`);
        return null;
      }
    },
    [rescan, dispatch, toast],
  );

  const renameNode = useCallback(
    async (projectId: string, from: string, to: string) => {
      try {
        await renamePath(projectId, from, to);
        buffers.renamePrefix(projectId, from, to);
        dispatch({ type: "PATH_RENAMED", projectId, from, to });
        await rescan(projectId);
      } catch (e) {
        toast("error", `Couldn't rename: ${String(e)}`);
      }
    },
    [dispatch, rescan, toast],
  );

  const deleteNode = useCallback(
    async (projectId: string, path: string) => {
      try {
        await deletePath(projectId, path);
        buffers.discardPrefix(projectId, path);
        dispatch({ type: "PATH_DELETED", projectId, path });
        await rescan(projectId);
      } catch (e) {
        toast("error", `Couldn't delete: ${String(e)}`);
      }
    },
    [dispatch, rescan, toast],
  );

  const duplicateNode = useCallback(
    async (projectId: string, path: string) => {
      try {
        const newRel = await duplicatePath(projectId, path);
        await rescan(projectId);
        if (newRel) await openFile(projectId, newRel);
        return newRel;
      } catch (e) {
        toast("error", `Couldn't duplicate: ${String(e)}`);
        return null;
      }
    },
    [rescan, openFile, toast],
  );

  const revealNode = useCallback(
    async (projectId: string, path: string) => {
      try {
        await reveal(projectId, path);
      } catch (e) {
        toast("error", `Couldn't reveal: ${String(e)}`);
      }
    },
    [toast],
  );

  const persistRuns = useCallback(
    async (projectId: string, runs: SavedRun[]) => {
      const project = state.projects[projectId];
      if (!project) return;
      try {
        await saveManifest(projectId, { ...project.manifest, runs });
      } catch (e) {
        toast("error", `Couldn't save: ${String(e)}`);
      }
    },
    [state.projects, toast],
  );

  // Debounces only the DISK WRITE for a run edit, never the reducer update —
  // a param/expression keystroke should feel instant, but shouldn't also
  // fire a saveManifest per character. Keyed per run so editing two runs'
  // fields at once (unlikely, but tabs allow it) debounces independently.
  // A ref (not module state) so it's scoped to this hook instance yet still
  // survives across the re-renders every dispatch causes.
  const runSaveTimers = useRef(new Map<string, number>());

  const upsertRun = useCallback(
    async (projectId: string, run: SavedRun, opts?: { debounceMs?: number }) => {
      const project = state.projects[projectId];
      if (!project) return;
      const runs = project.manifest.runs ?? [];
      const i = runs.findIndex((r) => r.id === run.id);
      const nextRuns = i >= 0 ? runs.map((r, idx) => (idx === i ? run : r)) : [...runs, run];
      dispatch({ type: "RUN_UPSERTED", projectId, run });

      const timerKey = `${projectId}::${run.id}`;
      window.clearTimeout(runSaveTimers.current.get(timerKey));
      const debounceMs = opts?.debounceMs ?? 0;
      if (debounceMs <= 0) {
        await persistRuns(projectId, nextRuns);
        return;
      }
      runSaveTimers.current.set(
        timerKey,
        window.setTimeout(() => void persistRuns(projectId, nextRuns), debounceMs),
      );
    },
    [state.projects, dispatch, persistRuns],
  );

  const newRun = useCallback(
    async (projectId: string, kind: RunKind, folder?: string) => {
      const run = { ...defaultRun(kind), folder };
      await upsertRun(projectId, run);
      openRun(projectId, run.id);
      return run.id;
    },
    [upsertRun, openRun],
  );

  const deleteRun = useCallback(
    async (projectId: string, runId: string) => {
      const project = state.projects[projectId];
      if (!project) return;
      const tabId = runTabId(projectId, runId);
      if (state.tabs[tabId]) results.clear(tabId);
      const nextRuns = (project.manifest.runs ?? []).filter((r) => r.id !== runId);
      dispatch({ type: "RUN_DELETED", projectId, runId });
      await persistRuns(projectId, nextRuns);
    },
    [state.projects, state.tabs, dispatch, persistRuns],
  );

  const renameRun = useCallback(
    async (projectId: string, runId: string) => {
      const run = state.projects[projectId]?.manifest.runs?.find((r) => r.id === runId);
      if (!run) return;
      const name = await prompt({ title: "Rename run", initialValue: run.name });
      if (name && name !== run.name) await upsertRun(projectId, { ...run, name });
    },
    [state.projects, prompt, upsertRun],
  );

  const duplicateRun = useCallback(
    async (projectId: string, runId: string) => {
      const run = state.projects[projectId]?.manifest.runs?.find((r) => r.id === runId);
      if (!run) return;
      const copy = { ...run, id: genRunId(), name: `${run.name} copy` };
      await upsertRun(projectId, copy);
      openRun(projectId, copy.id);
    },
    [state.projects, upsertRun, openRun],
  );

  const confirmDeleteNode = useCallback(
    async (projectId: string, path: string, isDir: boolean) => {
      const ok = await confirm({
        title: `Delete ${isDir ? "folder" : "file"}?`,
        message: `"${path}" will be permanently deleted. This can't be undone.`,
        confirmLabel: "Delete",
        danger: true,
      });
      if (ok) await deleteNode(projectId, path);
    },
    [confirm, deleteNode],
  );

  const confirmDeleteRun = useCallback(
    async (projectId: string, runId: string, name: string) => {
      const ok = await confirm({
        title: "Delete run?",
        message: `"${name}" will be permanently deleted.`,
        confirmLabel: "Delete",
        danger: true,
      });
      if (ok) await deleteRun(projectId, runId);
    },
    [confirm, deleteRun],
  );

  return {
    refreshKnownProjects,
    openDir,
    createAndOpen,
    promptNewProject,
    openFolderPicker,
    closeProject,
    forgetKnown,
    rescan,
    toggleNode,
    openFile,
    openRun,
    closeTab,
    closeOtherTabs,
    closeAllTabs,
    newFile,
    newFolder,
    renameNode,
    deleteNode,
    duplicateNode,
    revealNode,
    upsertRun,
    newRun,
    deleteRun,
    renameRun,
    duplicateRun,
    confirmDeleteNode,
    confirmDeleteRun,
    prompt,
  };
}
