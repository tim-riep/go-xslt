import type { Diagnostic, SecondaryOutput } from "../lib/api";

/** Shared across all three run kinds: a run's diagnostics list, clickable to
 * jump to the offending line when the diagnostic targets an open editor. */
export function DiagnosticsList({ diagnostics, onClick }: { diagnostics: Diagnostic[]; onClick?: (d: Diagnostic) => void }) {
  if (diagnostics.length === 0) return null;
  const errors = diagnostics.filter((d) => d.severity === "error");
  return (
    <div className="diagnostics">
      <div className="diagnostics-header">
        {errors.length} error{errors.length === 1 ? "" : "s"}, {diagnostics.length - errors.length} warning
        {diagnostics.length - errors.length === 1 ? "" : "s"}
      </div>
      <ul>
        {diagnostics.map((d, i) => (
          <li
            key={i}
            className={`diag diag-${d.severity}`}
            onClick={() => onClick?.(d)}
            title={d.line > 0 ? `Go to line ${d.line}` : undefined}
          >
            <span className="diag-loc">{d.line > 0 ? `${d.line}:${d.col || 1}` : "—"}</span>
            <span className="diag-code">{d.code || d.phase}</span>
            <span className="diag-msg">{d.message}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** xsl:message output from a transform run. */
export function MessagesList({ messages }: { messages: string[] }) {
  if (messages.length === 0) return null;
  return (
    <div className="messages">
      <div className="messages-header">
        {messages.length} message{messages.length === 1 ? "" : "s"}
      </div>
      <ul>
        {messages.map((m, i) => (
          <li key={i} className="msg-item">
            {m}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** xsl:result-document outputs from a transform run. */
export function SecondaryOutputsList({ outputs }: { outputs: SecondaryOutput[] }) {
  if (outputs.length === 0) return null;
  return (
    <div className="secondary">
      <div className="secondary-header">
        {outputs.length} result-document{outputs.length === 1 ? "" : "s"}
      </div>
      {outputs.map((s, i) => (
        <details key={i} className="secondary-doc">
          <summary>
            {s.href || `(result ${i + 1})`} · {s.method || "xml"}
          </summary>
          <pre className="output-text">{s.content}</pre>
        </details>
      ))}
    </div>
  );
}
