import { useCallback, useEffect, useRef, useState } from "react";
import { Panel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { FileKind, runValidate, XSDVersion, type SavedRun, type ValidateResult } from "../lib/api";
import { RUN_SHORTCUT } from "../lib/platform";
import { buffers, useBuffer } from "../state/buffers";
import { useProjectActions } from "../state/projectActions";
import { useRunner } from "../state/runner";
import { useAppState } from "../state/store";
import type { RunTab } from "../state/types";
import { Editor } from "./Editor";
import { DiagnosticsList } from "./ResultParts";
import { FilePickerField } from "./FilePickerField";
import { SchemaListEditor } from "./SchemaListEditor";

/** A saved "validate" run, against XSD 1.0 or 1.1. Both the schema and the
 * instance default to INLINE text typed straight into the run — no project
 * file required, so you can paste an XSD and an XML instance and validate
 * immediately. Either can be switched to a real project file instead (a
 * multi-file schema set, for xs:import/xs:include/xs:redefine across
 * files — or an instance that's also being edited as a plain tab); the
 * switch is non-destructive in both directions, so going back to inline
 * keeps whatever was last typed there. */
export function ValidateRunView({ tab, run }: { tab: RunTab; run: SavedRun }) {
  const state = useAppState();
  const actions = useProjectActions();
  const project = state.projects[tab.projectId];

  const schemas = run.schemas ?? [];
  const schemaInline = run.schemaInline ?? "";
  const instancePath = run.instance ?? "";
  const instanceInline = run.instanceInline ?? "";
  const schemaFileMode = schemas.length > 0;
  const instanceFileMode = instancePath !== "";
  // Run.XSDVersion is a plain string in the manifest (so the on-disk JSON
  // doesn't depend on the engine's enum shape); the <select> below only ever
  // writes "1.0"/"1.1", so this narrowing is safe.
  const version = (run.xsdVersion || XSDVersion.XSD10) as XSDVersion;

  useEffect(() => {
    if (instanceFileMode) void buffers.ensureLoaded(tab.projectId, instancePath);
  }, [tab.projectId, instancePath, instanceFileMode]);
  const instanceBuf = useBuffer(tab.projectId, instanceFileMode ? instancePath : null);

  const [autoRun, setAutoRun] = useState(true);

  const execute = useCallback(async () => {
    const schemaTexts = schemaFileMode
      ? await Promise.all(
          schemas.map(async (p) => {
            await buffers.ensureLoaded(tab.projectId, p);
            return buffers.get(tab.projectId, p).text;
          }),
        )
      : [schemaInline];
    const instanceText = instanceFileMode ? instanceBuf.text : instanceInline;
    return runValidate({ schemas: schemaTexts, instance: instanceText, version, baseDir: tab.projectId });
  }, [schemaFileMode, schemas, schemaInline, instanceFileMode, instanceBuf.text, instanceInline, version, tab.projectId]);

  const { run: doRun, status, result } = useRunner<ValidateResult>(tab.id, tab.projectId, execute);
  const running = status === "running";
  const hasSchema = schemaFileMode || schemaInline.trim() !== "";

  const debounceRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!autoRun) return;
    window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => void doRun(), 500);
    return () => window.clearTimeout(debounceRef.current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [instanceBuf.text, instanceInline, JSON.stringify(schemas), schemaInline, version, autoRun]);

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

  const diags = result?.diagnostics ?? [];

  return (
    <>
      <div className="params-bar">
        <label className="auto">
          Version{" "}
          <select value={version} onChange={(e) => void actions.upsertRun(tab.projectId, { ...run, xsdVersion: e.target.value })}>
            <option value={XSDVersion.XSD10}>1.0</option>
            <option value={XSDVersion.XSD11}>1.1</option>
          </select>
        </label>
        <span className="spacer" />
        <button className="primary" onClick={() => void doRun()} disabled={running || !hasSchema}>
          {running ? "Validating…" : `Validate ${RUN_SHORTCUT}`}
        </button>
        <label className="auto">
          <input type="checkbox" checked={autoRun} onChange={(e) => setAutoRun(e.target.checked)} /> Auto
        </label>
        {result && !running && (
          <span className={result.valid ? "valid-badge ok" : "valid-badge bad"}>
            {result.valid ? "✓ valid" : "✗ invalid"} · {result.durationMs} ms
          </span>
        )}
      </div>

      <PanelGroup direction="horizontal" className="panels">
        <Panel defaultSize={32} minSize={18}>
          <div className="pane">
            <div className="pane-header">
              <span className="pane-title">Schema</span>
              <span className="meta">{schemaFileMode ? `${schemas.length} file${schemas.length === 1 ? "" : "s"}` : "inline"}</span>
              <span className="spacer" />
              {schemaFileMode ? (
                <button
                  className="link-btn"
                  onClick={() => void actions.upsertRun(tab.projectId, { ...run, schemas: [] })}
                  title="Switch back to typing the schema directly"
                >
                  Use inline text
                </button>
              ) : (
                <FilePickerField
                  project={project}
                  value=""
                  kinds={[FileKind.KindSchema]}
                  placeholder="Use project file(s) instead"
                  className="link-btn"
                  onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, schemas: [p] })}
                />
              )}
            </div>
            {schemaFileMode ? (
              <SchemaListEditor
                project={project}
                value={schemas}
                onChange={(next) => void actions.upsertRun(tab.projectId, { ...run, schemas: next })}
              />
            ) : (
              <div className="editor-wrap">
                <Editor
                  value={schemaInline}
                  onChange={(v) => void actions.upsertRun(tab.projectId, { ...run, schemaInline: v }, { debounceMs: 400 })}
                />
              </div>
            )}
          </div>
        </Panel>
        <PanelResizeHandle className="resize-handle" />
        <Panel defaultSize={34} minSize={18}>
          <div className="pane">
            <div className="pane-header">
              <span className="pane-title">Instance XML</span>
              <span className="meta">{instanceFileMode ? instancePath : "inline"}</span>
              <span className="spacer" />
              {instanceFileMode ? (
                <button
                  className="link-btn"
                  onClick={() => void actions.upsertRun(tab.projectId, { ...run, instance: "" })}
                  title="Switch back to typing the instance directly"
                >
                  Use inline text
                </button>
              ) : (
                <FilePickerField
                  project={project}
                  value=""
                  kinds={[FileKind.KindXML]}
                  placeholder="Use a project file instead"
                  className="link-btn"
                  onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, instance: p })}
                />
              )}
            </div>
            <div className="editor-wrap">
              {instanceFileMode ? (
                <Editor value={instanceBuf.text} onChange={(v) => buffers.setText(tab.projectId, instancePath, v)} />
              ) : (
                <Editor
                  value={instanceInline}
                  onChange={(v) => void actions.upsertRun(tab.projectId, { ...run, instanceInline: v }, { debounceMs: 400 })}
                />
              )}
            </div>
          </div>
        </Panel>
        <PanelResizeHandle className="resize-handle" />
        <Panel defaultSize={34} minSize={18}>
          <div className="pane output-pane">
            <div className="pane-header">
              <span className="pane-title">Validation</span>
            </div>
            <div className="output-body output-body-scroll validation-result">
              {running && <div className="placeholder">Validating…</div>}
              {!running && result && (
                <div className={result.valid ? "validity ok" : "validity bad"}>
                  {result.valid ? "Instance is schema-valid." : "Instance is NOT schema-valid."}
                </div>
              )}
              {!running && !result && <div className="placeholder">Type or pick a schema and an instance, then Validate.</div>}
            </div>
            <DiagnosticsList diagnostics={diags} />
          </div>
        </Panel>
      </PanelGroup>
    </>
  );
}
