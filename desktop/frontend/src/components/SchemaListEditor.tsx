import { FileKind } from "../lib/api";
import type { ProjectState } from "../state/types";
import { FilePickerField } from "./FilePickerField";
import { Icon } from "./Icon";

/** The ordered, multi-file schema set a validate run compiles against — the
 * first entry is the entry schema, the rest are made available to its
 * xs:import/xs:include/xs:redefine (matching ValidateRequest.Schemas). */
export function SchemaListEditor({
  project,
  value,
  onChange,
}: {
  project: ProjectState;
  value: string[];
  onChange: (paths: string[]) => void;
}) {
  const move = (from: number, to: number) => {
    if (to < 0 || to >= value.length) return;
    const next = value.slice();
    const [item] = next.splice(from, 1);
    next.splice(to, 0, item);
    onChange(next);
  };

  return (
    <div className="schema-list">
      {value.length === 0 && <div className="sidebar-empty">No schema files yet — add at least one below.</div>}
      {value.map((path, i) => (
        <div key={`${path}-${i}`} className="schema-list-row">
          <span className="schema-list-row-index">{i + 1}</span>
          <span className={`schema-list-row-path${!project.nodes[path] ? " missing" : ""}`} title={path}>
            {path}
          </span>
          <button type="button" className="icon-btn" disabled={i === 0} onClick={() => move(i, i - 1)} title="Move up">
            ↑
          </button>
          <button
            type="button"
            className="icon-btn"
            disabled={i === value.length - 1}
            onClick={() => move(i, i + 1)}
            title="Move down"
          >
            ↓
          </button>
          <button
            type="button"
            className="icon-btn"
            onClick={() => onChange(value.filter((_, idx) => idx !== i))}
            title="Remove"
          >
            <Icon name="close" size={11} />
          </button>
        </div>
      ))}
      <FilePickerField
        project={project}
        value=""
        kinds={[FileKind.KindSchema]}
        placeholder="+ Add schema file"
        className="schema-list-add"
        onChange={(p) => onChange([...value, p])}
      />
    </div>
  );
}
