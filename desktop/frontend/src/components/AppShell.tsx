import { useEffect, useRef } from "react";
import { Panel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { createProject, listProjects, openProject } from "../lib/api";
import { useAppDispatch, useAppState } from "../state/store";
import { ContextMenuHost } from "./ContextMenuHost";
import { DialogHost } from "./DialogHost";
import { FileEditorView } from "./FileEditorView";
import { LicensesButton } from "./LicensesView";
import { Sidebar } from "./Sidebar";
import { TabStrip } from "./TabStrip";
import { Toaster } from "./Toaster";
import { TransformRunView } from "./TransformRunView";
import { ValidateRunView } from "./ValidateRunView";
import { XpathRunView } from "./XpathRunView";

/** Runs once, ever, for the life of the app window: loads the known-project
 * registry and, if nothing has ever been opened, seeds and opens a starter
 * "Scratch" project so the app is immediately usable with zero setup;
 * otherwise reopens the most-recently-used project. Guarded by a ref (not
 * just an empty dependency array) because React 18 StrictMode intentionally
 * double-invokes effects in development — without the guard, a fresh
 * install would get two "Scratch" projects. */
function useBootstrap() {
  const dispatch = useAppDispatch();
  const ran = useRef(false);
  useEffect(() => {
    if (ran.current) return;
    ran.current = true;
    void (async () => {
      const refs = await listProjects();
      dispatch({ type: "KNOWN_PROJECTS_LOADED", refs: refs ?? [] });
      const project = refs && refs.length > 0 ? await openProject(refs[0].dir) : await createProject("Scratch");
      if (project) dispatch({ type: "PROJECT_OPENED", project });
      const refreshed = await listProjects();
      dispatch({ type: "KNOWN_PROJECTS_LOADED", refs: refreshed ?? [] });
    })();
  }, [dispatch]);
}

export function AppShell() {
  useBootstrap();
  const state = useAppState();

  const activeProject = state.activeProjectId ? state.projects[state.activeProjectId] : null;
  const activeTab = state.activeTabId ? state.tabs[state.activeTabId] : null;
  const activeRun =
    activeTab?.kind === "run" ? state.projects[activeTab.projectId]?.manifest.runs?.find((r) => r.id === activeTab.runId) : null;

  return (
    <div className="app">
      <header className="toolbar">
        <span className="brand">go-xslt</span>
        <span className="spacer" />
        <LicensesButton />
      </header>
      <div className="app-body">
        <PanelGroup direction="horizontal" className="panels">
          <Panel defaultSize={18} minSize={12} maxSize={40}>
            <Sidebar />
          </Panel>
          <PanelResizeHandle className="resize-handle" />
          <Panel defaultSize={82} minSize={40}>
            <div className="main">
              <TabStrip />
              <div className="main-content">
                {!activeProject && (
                  <div className="empty-state">
                    <div className="empty-state-title">No project open</div>
                    <div>Create or open one from the sidebar.</div>
                  </div>
                )}
                {activeProject && !activeTab && (
                  <div className="empty-state">
                    <div className="empty-state-title">Nothing open</div>
                    <div>Pick a file or a saved run from the sidebar.</div>
                  </div>
                )}
                {activeProject && activeTab?.kind === "file" && <FileEditorView key={activeTab.id} tab={activeTab} />}
                {activeProject && activeTab?.kind === "run" && activeRun?.kind === "transform" && (
                  <TransformRunView key={activeTab.id} tab={activeTab} run={activeRun} />
                )}
                {activeProject && activeTab?.kind === "run" && activeRun?.kind === "validate" && (
                  <ValidateRunView key={activeTab.id} tab={activeTab} run={activeRun} />
                )}
                {activeProject && activeTab?.kind === "run" && activeRun?.kind === "xpath" && (
                  <XpathRunView key={activeTab.id} tab={activeTab} run={activeRun} />
                )}
                {activeProject && activeTab?.kind === "run" && !activeRun && (
                  <div className="empty-state">
                    <div className="empty-state-title">Run not found</div>
                    <div>It may have just been deleted.</div>
                  </div>
                )}
              </div>
            </div>
          </Panel>
        </PanelGroup>
      </div>
      <DialogHost />
      <ContextMenuHost />
      <Toaster />
    </div>
  );
}
