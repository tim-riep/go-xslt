import { useEffect, useMemo, useRef, useState } from "react";
import type { FileKind } from "../lib/api";
import type { ProjectState } from "../state/types";

/** A button showing the currently-picked project-relative path; clicking it
 * opens a dropdown of every file in the project matching `kinds` (omit for
 * any file). Reference-only — it does not open the file for editing itself,
 * callers that also want an inline editor combine this with useBuffer. */
export function FilePickerField({
  project,
  value,
  kinds,
  placeholder = "Choose file…",
  className = "file-picker-field",
  onChange,
}: {
  project: ProjectState;
  value: string;
  kinds?: FileKind[];
  placeholder?: string;
  className?: string;
  onChange: (path: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", close);
    return () => window.removeEventListener("mousedown", close);
  }, [open]);

  const options = useMemo(
    () =>
      Object.values(project.nodes)
        .filter((n) => !n.dir && (!kinds || (n.kind && kinds.includes(n.kind))))
        .sort((a, b) => a.path.localeCompare(b.path)),
    [project.nodes, kinds],
  );

  const missing = value !== "" && !project.nodes[value];

  return (
    <div style={{ position: "relative", display: "inline-block" }} ref={ref}>
      <button type="button" className={className} onClick={() => setOpen((o) => !o)}>
        {value ? (
          <span className={`file-picker-field-path${missing ? " missing" : ""}`}>{value}</span>
        ) : (
          <span className="file-picker-field-empty">{placeholder}</span>
        )}
      </button>
      {open && (
        <div className="project-switcher-menu" style={{ minWidth: 240, left: 0, right: "auto" }}>
          {options.length === 0 && <div className="sidebar-empty">No matching files in this project.</div>}
          {options.map((n) => (
            <div
              key={n.path}
              className={`project-switcher-item${n.path === value ? " active" : ""}`}
              onClick={() => {
                onChange(n.path);
                setOpen(false);
              }}
              title={n.path}
            >
              {n.path}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
