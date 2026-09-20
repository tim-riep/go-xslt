import { useMemo, useState } from "react";
import { ReadOnlyViewer } from "./Editor";
import { formatXml } from "../lib/format";
import type { Diagnostic, TransformResult } from "../lib/api";
import { DiagnosticsList, MessagesList, SecondaryOutputsList } from "./ResultParts";

interface OutputPaneProps {
  result: TransformResult | null;
  running: boolean;
  onDiagnosticClick?: (d: Diagnostic) => void;
}

export function OutputPane({ result, running, onDiagnosticClick }: OutputPaneProps) {
  const [tab, setTab] = useState<"output" | "rendered">("output");
  const [pretty, setPretty] = useState(true);
  const diags = result?.diagnostics ?? [];
  const method = result?.method || "xml";
  const canRender = method === "html";
  const isMarkup = method === "xml" || method === "html";
  const messages = result?.messages ?? [];
  const secondary = result?.secondaryOutputs ?? [];

  const raw = result?.output ?? "";
  const shown = useMemo(
    () => (pretty && isMarkup ? formatXml(raw) : raw),
    [raw, pretty, isMarkup],
  );

  return (
    <div className="pane output-pane">
      <div className="pane-header">
        <span className="pane-title">Output</span>
        <div className="tabs">
          <button className={tab === "output" ? "tab active" : "tab"} onClick={() => setTab("output")}>
            Source
          </button>
          {canRender && (
            <button className={tab === "rendered" ? "tab active" : "tab"} onClick={() => setTab("rendered")}>
              Rendered
            </button>
          )}
        </div>
        {isMarkup && tab === "output" && (
          <label className="auto" title="Pretty-print the output">
            <input type="checkbox" checked={pretty} onChange={(e) => setPretty(e.target.checked)} /> Format
          </label>
        )}
        <span className="spacer" />
        {result && (
          <span className="meta">
            {method} · {result.durationMs} ms
          </span>
        )}
      </div>

      <div className="output-body">
        {running && <div className="placeholder">Running…</div>}
        {!running && tab === "output" && (
          <ReadOnlyViewer value={shown} />
        )}
        {!running && tab === "rendered" && canRender && (
          <iframe className="render-frame" title="rendered" sandbox="" srcDoc={raw} />
        )}
      </div>

      <MessagesList messages={messages} />
      <SecondaryOutputsList outputs={secondary} />
      <DiagnosticsList diagnostics={diags} onClick={onDiagnosticClick} />
    </div>
  );
}
