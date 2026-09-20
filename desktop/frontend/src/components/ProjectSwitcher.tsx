import { useEffect, useRef, useState } from "react";
import { useAppDispatch, useAppState } from "../state/store";
import { useProjectActions } from "../state/projectActions";
import { Icon } from "./Icon";

export function ProjectSwitcher() {
  const state = useAppState();
  const dispatch = useAppDispatch();
  const actions = useProjectActions();
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

  const active = state.activeProjectId ? state.projects[state.activeProjectId] : null;

  return (
    <div className="project-switcher" ref={ref}>
      <button className="project-switcher-trigger" onClick={() => setOpen((o) => !o)}>
        <span className="project-switcher-name">{active?.name ?? "No project open"}</span>
        <Icon
          name="chevron-right"
          className="project-switcher-caret"
          style={{ transform: open ? "rotate(90deg)" : undefined, transition: "transform .1s ease" }}
        />
      </button>
      {open && (
        <div className="project-switcher-menu">
          {state.knownProjects.length === 0 && <div className="sidebar-empty">No projects yet.</div>}
          {state.knownProjects.map((ref_) => (
            <div
              key={ref_.dir}
              className={`project-switcher-item${ref_.dir === state.activeProjectId ? " active" : ""}`}
              onClick={async () => {
                setOpen(false);
                if (state.projects[ref_.dir]) dispatch({ type: "PROJECT_ACTIVATED", projectId: ref_.dir });
                else await actions.openDir(ref_.dir);
              }}
              title={ref_.dir}
            >
              {ref_.name || ref_.dir.split("/").pop()}
            </div>
          ))}
          <div className="project-switcher-sep" />
          <div
            className="project-switcher-action"
            onClick={async () => {
              setOpen(false);
              await actions.promptNewProject();
            }}
          >
            + New project…
          </div>
          <div
            className="project-switcher-action"
            onClick={async () => {
              setOpen(false);
              await actions.openFolderPicker();
            }}
          >
            Open folder…
          </div>
        </div>
      )}
    </div>
  );
}
