import { useCallback, useEffect, useRef, useState } from "react";
import { Panel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { FileKind, runXPath, type SavedRun, type XPathResult } from "../lib/api";
import { RUN_SHORTCUT } from "../lib/platform";
import { buffers, useBuffer } from "../state/buffers";
import { useProjectActions } from "../state/projectActions";
import { useRunner } from "../state/runner";
import { useAppState } from "../state/store";
import type { RunTab } from "../state/types";
import { Editor } from "./Editor";
import { DiagnosticsList } from "./ResultParts";
import { FilePickerField } from "./FilePickerField";

/** A saved "xpath" run: a standalone XPath 3.1 expression, evaluated against
 * a context document that defaults to INLINE text typed straight into the
 * run — no project file required — or switched to a real project file
 * instead (non-destructively either way). Leave the context empty for
 * expressions needing no input (e.g. "1 + 2", "current-dateTime()"). */
export function XpathRunView({ tab, run }: { tab: RunTab; run: SavedRun }) {
  const state = useAppState();
  const actions = useProjectActions();
  const project = state.projects[tab.projectId];

  const expression = run.expression ?? "";
  const contextPath = run.contextDoc ?? "";
  const contextInline = run.contextInline ?? "";
  const contextFileMode = contextPath !== "";

  useEffect(() => {
    if (contextFileMode) void buffers.ensureLoaded(tab.projectId, contextPath);
  }, [tab.projectId, contextPath, contextFileMode]);
  const contextBuf = useBuffer(tab.projectId, contextFileMode ? contextPath : null);

  const [autoRun, setAutoRun] = useState(true);

  const execute = useCallback(
    () => runXPath({ expression, source: contextFileMode ? contextBuf.text : contextInline, baseDir: tab.projectId }),
    [expression, contextFileMode, contextBuf.text, contextInline, tab.projectId],
  );

  const { run: doRun, status, result } = useRunner<XPathResult>(tab.id, tab.projectId, execute);
  const running = status === "running";

  const debounceRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!autoRun || !expression.trim()) return;
    window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => void doRun(), 400);
    return () => window.clearTimeout(debounceRef.current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expression, contextBuf.text, contextInline, contextFileMode, autoRun]);

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
  const items = result?.items ?? [];

  return (
    <>
      <div className="params-bar xpath-bar">
        <span className="params-label">XPath 3.1</span>
        <button className="param-add" onClick={() => void doRun()} disabled={running}>
          {running ? "…" : `Eval ${RUN_SHORTCUT}`}
        </button>
        <label className="auto">
          <input type="checkbox" checked={autoRun} onChange={(e) => setAutoRun(e.target.checked)} /> Auto
        </label>
        <span className="spacer" />
        {result && !running && (
          <span className="meta">
            {result.count} item{result.count === 1 ? "" : "s"} · {result.durationMs} ms
          </span>
        )}
      </div>

      <PanelGroup direction="horizontal" className="panels">
        <Panel defaultSize={48} minSize={20}>
          <PanelGroup direction="vertical">
            <Panel defaultSize={45} minSize={15}>
              <div className="pane">
                <div className="pane-header">
                  <span className="pane-title">XPath expression</span>
                  <span className="meta">{RUN_SHORTCUT} to evaluate</span>
                </div>
                <textarea
                  className="xpath-editor"
                  value={expression}
                  onChange={(e) => void actions.upsertRun(tab.projectId, { ...run, expression: e.target.value }, { debounceMs: 400 })}
                  placeholder={"XPath 3.1 expression, e.g.\n//book[@price > 30]/title\nor\nstring-join((1 to 5) ! string(), ',')"}
                  spellCheck={false}
                  onKeyDown={(e) => {
                    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                      e.preventDefault();
                      void doRun();
                    }
                  }}
                />
              </div>
            </Panel>
            <PanelResizeHandle className="resize-handle vertical" />
            <Panel defaultSize={55} minSize={15}>
              <div className="pane">
                <div className="pane-header">
                  <span className="pane-title">Context document</span>
                  <span className="meta">{contextFileMode ? contextPath : "inline"}</span>
                  <span className="spacer" />
                  {contextFileMode ? (
                    <button
                      className="link-btn"
                      onClick={() => void actions.upsertRun(tab.projectId, { ...run, contextDoc: "" })}
                      title="Switch back to typing the context document directly"
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
                      onChange={(p) => void actions.upsertRun(tab.projectId, { ...run, contextDoc: p })}
                    />
                  )}
                </div>
                <div className="editor-wrap">
                  {contextFileMode ? (
                    <Editor value={contextBuf.text} onChange={(v) => buffers.setText(tab.projectId, contextPath, v)} />
                  ) : (
                    <Editor
                      value={contextInline}
                      onChange={(v) => void actions.upsertRun(tab.projectId, { ...run, contextInline: v }, { debounceMs: 400 })}
                    />
                  )}
                </div>
              </div>
            </Panel>
          </PanelGroup>
        </Panel>
        <PanelResizeHandle className="resize-handle" />
        <Panel defaultSize={52} minSize={20}>
          <div className="pane output-pane">
            <div className="pane-header">
              <span className="pane-title">Result</span>
              {result && !running && diags.length === 0 && <span className="meta">value: {truncate(result.value)}</span>}
            </div>
            <div className="output-body output-body-scroll">
              {running && <div className="placeholder">Evaluating…</div>}
              {!running && diags.length === 0 && items.length > 0 && (
                <table className="xpath-items">
                  <thead>
                    <tr>
                      <th className="idx">#</th>
                      <th className="ty">type</th>
                      <th>value</th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((it, i) => (
                      <tr key={i}>
                        <td className="idx">{i + 1}</td>
                        <td className="ty">{it.type}</td>
                        <td className="val">{it.value}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {!running && diags.length === 0 && items.length === 0 && result && <div className="placeholder">empty sequence</div>}
              {!running && !result && <div className="placeholder">Type an expression above.</div>}
            </div>
            <DiagnosticsList diagnostics={diags} />
          </div>
        </Panel>
      </PanelGroup>
    </>
  );
}

function truncate(s: string, n = 60): string {
  return s.length > n ? s.slice(0, n) + "…" : s;
}
