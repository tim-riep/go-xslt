import { useCallback, useEffect, useRef, useState } from "react";
import { Panel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { FileKind, runTransform, type Diagnostic, type SavedRun, type TransformResult } from "../lib/api";
import { RUN_SHORTCUT } from "../lib/platform";
import { buffers, useBuffer } from "../state/buffers";
import { useProjectActions } from "../state/projectActions";
import { useRunner } from "../state/runner";
import { useAppState } from "../state/store";
import type { RunTab } from "../state/types";
import { Editor } from "./Editor";
import { FilePickerField } from "./FilePickerField";
import { OutputPane } from "./OutputPane";

/** A saved "transform" run: entry stylesheet + source XML + stylesheet
 * params, editable inline (both files are ordinary project buffers, so
 * edits here autosave and stay in sync with the same file open elsewhere as
 * a plain tab) with a live-updating transform output. */
export function TransformRunView({ tab, run }: { tab: RunTab; run: SavedRun }) {
  const state = useAppState();
  const actions = useProjectActions();
  const project = state.projects[tab.projectId];

  const stylesheetPath = run.stylesheet ?? "";
  const sourcePath = run.source ?? "";
  const params = run.params ?? {};

  useEffect(() => {
    if (stylesheetPath) void buffers.ensureLoaded(tab.projectId, stylesheetPath);
  }, [tab.projectId, stylesheetPath]);
  useEffect(() => {
    if (sourcePath) void buffers.ensureLoaded(tab.projectId, sourcePath);
  }, [tab.projectId, sourcePath]);

  const stylesheetBuf = useBuffer(tab.projectId, stylesheetPath || null);
  const sourceBuf = useBuffer(tab.projectId, sourcePath || null);

  const [autoRun, setAutoRun] = useState(true);
  const [newParam, setNewParam] = useState("");

  const execute = useCallback(
    () =>
      runTransform({
        stylesheet: stylesheetBuf.text,
        source: sourceBuf.text,
        params,
        initialTemplate: run.initialTemplate ?? "",
        baseDir: tab.projectId,
      }),
    [stylesheetBuf.text, sourceBuf.text, params, run.initialTemplate, tab.projectId],
  );

  const { run: doRun, status, result } = useRunner<TransformResult>(tab.id, tab.projectId, execute);
  const running = status === "running";

  const debounceRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!autoRun || !stylesheetPath) return;
    window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => void doRun(), 500);
    return () => window.clearTimeout(debounceRef.current);
    // Re-run whenever the resolved content, params, or entry-template choice
    // change; `doRun`'s own identity already reflects all of these.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stylesheetBuf.text, sourceBuf.text, JSON.stringify(params), run.initialTemplate, autoRun, stylesheetPath]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault();
        void doRun();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [doRun]);

  if (!project) return null;

  const setParam = (name: string, value: string) => {
    void actions.upsertRun(tab.projectId, { ...run, params: { ...params, [name]: value } }, { debounceMs: 500 });
  };
  const commitNewParam = () => {
    const name = newParam.trim();
    if (!name || params[name] !== undefined) return;
    void actions.upsertRun(tab.projectId, { ...run, params: { ...params, [name]: "" } });
    setNewParam("");
  };
  const removeParam = (name: string) => {
    const next = { ...params };
    delete next[name];
    void actions.upsertRun(tab.projectId, { ...run, params: next });
  };

  const styDiags: Diagnostic[] = (result?.diagnostics ?? []).filter((d) => d.phase === "parse" || d.phase === "compile");

  return (
    <>
      <div className="params-bar">
        <FilePickerField
          project={project}
          value={stylesheetPath}
          kinds={[FileKind.KindStylesheet]}
          placeholder="Choose stylesheet…"
          onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, stylesheet: p })}
        />
        <FilePickerField
          project={project}
          value={sourcePath}
          kinds={[FileKind.KindXML]}
          placeholder="Choose source XML…"
          onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, source: p })}
        />
        <span className="spacer" />
        <button className="primary" onClick={() => void doRun()} disabled={running || !stylesheetPath}>
          {running ? "Running…" : `Run ${RUN_SHORTCUT}`}
        </button>
        <label className="auto">
          <input type="checkbox" checked={autoRun} onChange={(e) => setAutoRun(e.target.checked)} /> Auto
        </label>
      </div>

      <div className="params-bar">
        <span className="params-label">Params</span>
        {Object.entries(params).length === 0 && <span className="params-empty">none</span>}
        {Object.entries(params).map(([name, value]) => (
          <span className="param" key={name}>
            <code>{name}</code>
            <input value={value} onChange={(e) => setParam(name, e.target.value)} placeholder="value" />
            <button className="param-del" onClick={() => removeParam(name)}>
              ×
            </button>
          </span>
        ))}
        <span className="param param-new">
          <input
            className="param-new-name"
            value={newParam}
            onChange={(e) => setNewParam(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                commitNewParam();
              }
            }}
            placeholder="new param name"
          />
          <button className="param-add" onClick={commitNewParam} disabled={!newParam.trim()}>
            + add
          </button>
        </span>
      </div>

      <PanelGroup direction="horizontal" className="panels">
        <Panel defaultSize={36} minSize={15}>
          <div className="pane">
            <div className="pane-header">
              <span className="pane-title">Stylesheet</span>
              <span className="meta">{stylesheetPath || "none picked"}</span>
            </div>
            <div className="editor-wrap">
              {stylesheetPath ? (
                <Editor
                  value={stylesheetBuf.text}
                  onChange={(v) => buffers.setText(tab.projectId, stylesheetPath, v)}
                  diagnostics={styDiags}
                />
              ) : (
                <div className="placeholder">Pick a stylesheet above.</div>
              )}
            </div>
          </div>
        </Panel>
        <PanelResizeHandle className="resize-handle" />
        <Panel defaultSize={28} minSize={15}>
          <div className="pane">
            <div className="pane-header">
              <span className="pane-title">Source XML</span>
              <span className="meta">{sourcePath || "none picked"}</span>
            </div>
            <div className="editor-wrap">
              {sourcePath ? (
                <Editor value={sourceBuf.text} onChange={(v) => buffers.setText(tab.projectId, sourcePath, v)} />
              ) : (
                <div className="placeholder">Pick a source XML above (optional if the stylesheet uses an initial template).</div>
              )}
            </div>
          </div>
        </Panel>
        <PanelResizeHandle className="resize-handle" />
        <Panel defaultSize={36} minSize={15}>
          <OutputPane result={result} running={running} />
        </Panel>
      </PanelGroup>
    </>
  );
}
