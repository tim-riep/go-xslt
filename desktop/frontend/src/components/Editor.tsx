import { useMemo } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { xml } from "@codemirror/lang-xml";
import { linter, lintGutter, type Diagnostic as CMDiagnostic } from "@codemirror/lint";
import { EditorView } from "@codemirror/view";
import { githubDark } from "@uiw/codemirror-theme-github";
import type { Diagnostic } from "../lib/api";

interface EditorProps {
  value: string;
  onChange: (value: string) => void;
  /** Engine diagnostics to render in the gutter / inline (already filtered to this document). */
  diagnostics?: Diagnostic[];
  readOnly?: boolean;
}

/** Convert a 1-based (line, col) to a 0-based document offset. */
function offsetOf(doc: { line: (n: number) => { from: number; length: number }; lines: number }, line: number, col: number): number {
  const ln = Math.min(Math.max(line, 1), doc.lines);
  const lineObj = doc.line(ln);
  const c = Math.max(col, 1) - 1;
  return lineObj.from + Math.min(c, lineObj.length);
}

/** ReadOnlyViewer renders text (formatted output) in a read-only, syntax-
 * highlighted CodeMirror with no editing affordances. */
export function ReadOnlyViewer({ value }: { value: string }) {
  return (
    <CodeMirror
      value={value}
      theme={githubDark}
      height="100%"
      style={{ height: "100%", fontSize: 13 }}
      extensions={[xml(), EditorView.lineWrapping, EditorView.editable.of(false)]}
      editable={false}
      basicSetup={{
        lineNumbers: true,
        foldGutter: true,
        highlightActiveLine: false,
        highlightActiveLineGutter: false,
      }}
    />
  );
}

/** XSLT is XML, so the XML language mode + a diagnostics linter cover it. */
export function Editor({ value, onChange, diagnostics = [], readOnly = false }: EditorProps) {
  const lintExt = useMemo(
    () =>
      linter((view): CMDiagnostic[] => {
        const doc = view.state.doc;
        return diagnostics
          .filter((d) => d.line > 0)
          .map((d) => {
            const from = offsetOf(doc as any, d.line, d.col || 1);
            // Highlight to end of the line when no precise column span is known.
            const lineObj = (doc as any).line(Math.min(Math.max(d.line, 1), (doc as any).lines));
            const to = d.col > 0 ? Math.min(from + 1, lineObj.from + lineObj.length) : lineObj.from + lineObj.length;
            return {
              from,
              to: Math.max(to, from),
              severity: d.severity === "error" ? "error" : "warning",
              message: d.code ? `[${d.code}] ${d.message}` : d.message,
            } as CMDiagnostic;
          });
      }),
    [diagnostics],
  );

  return (
    <CodeMirror
      value={value}
      theme={githubDark}
      height="100%"
      style={{ height: "100%", fontSize: 13 }}
      extensions={[xml(), lintGutter(), lintExt, EditorView.lineWrapping]}
      onChange={onChange}
      readOnly={readOnly}
      basicSetup={{ lineNumbers: true, foldGutter: true, highlightActiveLine: !readOnly }}
    />
  );
}
