import { useCallback, useEffect, useRef, useState } from "react";
import type { DockviewApi } from "dockview-react";
import { FileKind, runTransform, type Diagnostic, type SavedRun, type TransformResult } from "../lib/api";
import { RUN_SHORTCUT } from "../lib/platform";
import { useT } from "../i18n";
import { runsMessages } from "../i18n/messages/runs";
import { buffers, useBuffer } from "../state/buffers";
import { useProjectActions } from "../state/projectActions";
import { useRunner } from "../state/runner";
import { useAppState } from "../state/store";
import type { RunTab } from "../state/types";
import { DockLayout, setWidths, type DockPane } from "./DockLayout";
import { Editor } from "./Editor";
import { FilePickerField } from "./FilePickerField";
import { OutputPane } from "./OutputPane";

/** The default arrangement: stylesheet | source | output, side by side. */
function arrangeTransform(api: DockviewApi) {
  api.addPanel({ id: "stylesheet", component: "pane", title: "Stylesheet" });
  api.addPanel({ id: "source", component: "pane", title: "Source XML", position: { referencePanel: "stylesheet", direction: "right" } });
  api.addPanel({ id: "output", component: "pane", title: "Output", position: { referencePanel: "source", direction: "right" } });
  setWidths(api, ["stylesheet", "source", "output"], [36, 28, 36]);
}

/** A saved "transform" run: entry stylesheet + source XML + stylesheet
 * params, editable inline (both files are ordinary project buffers, so
 * edits here autosave and stay in sync with the same file open elsewhere as
 * a plain tab) with a live-updating transform output. */
export function TransformRunView({ tab, run }: { tab: RunTab; run: SavedRun }) {
  const state = useAppState();
  const actions = useProjectActions();
  const project = state.projects[tab.projectId];
  const t = useT(runsMessages);

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
        // The project-relative path; the Go side resolves it to the file.
        sourceURI: sourcePath,
        params,
        initialTemplate: run.initialTemplate ?? "",
        baseDir: tab.projectId,
        // No network access; files only from the project folder.
        security: { allowNetwork: false },
        features: {
          schemaAware: !!run.schemaAware,
          backwardsCompatible: !!run.backwardsCompatible,
          strictStreaming: !!run.strictStreaming,
        },
        // The engine's defaults: 20 million instructions, nesting 10,000.
        limits: { maxSteps: 0, maxDepth: 0, maxOutputBytes: 0 },
      }),
    [stylesheetBuf.text, sourceBuf.text, sourcePath, params, run.initialTemplate, tab.projectId, run.schemaAware, run.backwardsCompatible, run.strictStreaming],
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
  }, [
    stylesheetBuf.text,
    sourceBuf.text,
    JSON.stringify(params),
    run.initialTemplate,
    autoRun,
    stylesheetPath,
    run.schemaAware,
    run.backwardsCompatible,
    run.strictStreaming,
  ]);

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

  const panes: DockPane[] = [
    {
      id: "stylesheet",
      title: t("paneStylesheet"),
      content: (
        <div className="pane">
          <div className="pane-header">
            <span className="pane-title">{t("paneStylesheet")}</span>
            <span className="meta">{stylesheetPath || t("nonePicked")}</span>
          </div>
          <div className="editor-wrap">
            {stylesheetPath ? (
              <Editor
                value={stylesheetBuf.text}
                onChange={(v) => buffers.setText(tab.projectId, stylesheetPath, v)}
                diagnostics={styDiags}
              />
            ) : (
              <div className="placeholder">{t("pickStylesheetAbove")}</div>
            )}
          </div>
        </div>
      ),
    },
    {
      id: "source",
      title: t("paneSource"),
      content: (
        <div className="pane">
          <div className="pane-header">
            <span className="pane-title">{t("paneSource")}</span>
            <span className="meta">{sourcePath || t("nonePicked")}</span>
          </div>
          <div className="editor-wrap">
            {sourcePath ? (
              <Editor value={sourceBuf.text} onChange={(v) => buffers.setText(tab.projectId, sourcePath, v)} />
            ) : (
              <div className="placeholder">{t("pickSourceAbove")}</div>
            )}
          </div>
        </div>
      ),
    },
    { id: "output", title: t("paneOutput"), content: <OutputPane result={result} running={running} /> },
  ];

  return (
    <>
      <div className="params-bar">
        <FilePickerField
          project={project}
          value={stylesheetPath}
          kinds={[FileKind.KindStylesheet]}
          placeholder={t("chooseStylesheet")}
          onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, stylesheet: p })}
        />
        <FilePickerField
          project={project}
          value={sourcePath}
          kinds={[FileKind.KindXML]}
          placeholder={t("chooseSource")}
          onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, source: p })}
        />
        <FeatureSwitches
          run={run}
          onChange={(next) => void actions.upsertRun(tab.projectId, { ...run, ...next })}
        />
        <span className="spacer" />
        <button className="primary" onClick={() => void doRun()} disabled={running || !stylesheetPath}>
          {running ? t("running") : t("runButton", { shortcut: RUN_SHORTCUT })}
        </button>
        <label className="auto">
          <input type="checkbox" checked={autoRun} onChange={(e) => setAutoRun(e.target.checked)} /> {t("auto")}
        </label>
      </div>

      <div className="params-panel">
        <div className="params-panel-header">
          <span className="params-label">{t("params")}</span>
          {Object.entries(params).length === 0 && <span className="params-empty">{t("paramsNone")}</span>}
        </div>
        <div className="params-list">
          {Object.entries(params).map(([name, value]) => (
            <div className="param-row" key={name}>
              <code className="param-row-name" title={name}>
                {name}
              </code>
              <textarea
                className="param-row-value"
                value={value}
                onChange={(e) => setParam(name, e.target.value)}
                placeholder={t("paramValue")}
                rows={1}
                spellCheck={false}
              />
              <button className="param-del" onClick={() => removeParam(name)}>
                ×
              </button>
            </div>
          ))}
          <div className="param-row param-row-new">
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
              placeholder={t("newParamName")}
            />
            <button className="param-add" onClick={commitNewParam} disabled={!newParam.trim()}>
              {t("addParam")}
            </button>
          </div>
        </div>
      </div>

      <DockLayout storageKey="transform" panes={panes} arrange={arrangeTransform} />
    </>
  );
}

type FeatureKey = "schemaAware" | "backwardsCompatible" | "strictStreaming";
type RunsKey = keyof typeof runsMessages.en;

const FEATURES: { key: FeatureKey; label: RunsKey; title: RunsKey }[] = [
  { key: "schemaAware", label: "featSchemaAware", title: "featSchemaAwareTitle" },
  { key: "backwardsCompatible", label: "featBackCompat", title: "featBackCompatTitle" },
  { key: "strictStreaming", label: "featStrictStreaming", title: "featStrictStreamingTitle" },
];

/** The optional XSLT 3.0 features this run's stylesheet is compiled with
 * (engine.Features), saved with the run. */
function FeatureSwitches({ run, onChange }: { run: SavedRun; onChange: (next: Partial<SavedRun>) => void }) {
  const t = useT(runsMessages);
  return (
    <span className="feature-switches">
      {FEATURES.map((f) => (
        <label key={f.key} className="auto" title={t(f.title)}>
          <input type="checkbox" checked={!!run[f.key]} onChange={(e) => onChange({ [f.key]: e.target.checked })} />{" "}
          {t(f.label)}
        </label>
      ))}
    </span>
  );
}
