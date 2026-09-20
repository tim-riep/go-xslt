import { useEffect, useRef } from "react";
import { buffers, useBuffer } from "../state/buffers";
import { useProjectActions } from "../state/projectActions";
import { useAppState } from "../state/store";
import type { FileTab } from "../state/types";
import { Editor } from "./Editor";
import { Icon } from "./Icon";

/** A plain project file opened for editing — any extension, not just the
 * ones a run references. Autosaves through the shared buffer store. */
export function FileEditorView({ tab }: { tab: FileTab }) {
  const state = useAppState();
  const actions = useProjectActions();
  const project = state.projects[tab.projectId];
  const buf = useBuffer(tab.projectId, tab.path);

  useEffect(() => {
    void buffers.ensureLoaded(tab.projectId, tab.path);
  }, [tab.projectId, tab.path]);

  // A file recreated by editing a "missing" tab (autosave writes it back to
  // disk) still shows the missing banner until the tree catches up — nudge
  // one rescan right after that first successful save.
  const wasSaving = useRef(false);
  useEffect(() => {
    if (wasSaving.current && !buf.saving && !buf.error && tab.missing) void actions.rescan(tab.projectId);
    wasSaving.current = buf.saving;
  }, [buf.saving, buf.error, tab.missing, tab.projectId, actions]);

  if (!project) return null;

  const dirty = buf.version !== buf.savedVersion;

  return (
    <div className="pane">
      {tab.missing && (
        <div className="missing-banner">
          <Icon name="warning" />
          <span>This file no longer exists on disk — editing and saving will recreate it at the same path.</span>
        </div>
      )}
      <div className="pane-header">
        <span className="pane-title">{tab.path.split("/").pop()}</span>
        <span className="meta">{tab.path}</span>
        <span className="spacer" />
        {buf.error && <span className="meta" style={{ color: "var(--error)" }}>Save failed: {buf.error}</span>}
        {!buf.error && buf.saving && <span className="meta">Saving…</span>}
        {!buf.error && !buf.saving && dirty && <span className="meta">Unsaved…</span>}
        {!buf.error && !buf.saving && !dirty && <span className="meta">Saved</span>}
      </div>
      <div className="editor-wrap">
        <Editor value={buf.text} onChange={(v) => buffers.setText(tab.projectId, tab.path, v)} />
      </div>
    </div>
  );
}
