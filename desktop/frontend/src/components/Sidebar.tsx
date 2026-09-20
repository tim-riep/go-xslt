import { useAppDispatch, useAppState } from "../state/store";
import { ProjectSwitcher } from "./ProjectSwitcher";
import { FileTree } from "./FileTree";
import { RunList } from "./RunList";

export function Sidebar() {
  const state = useAppState();
  const dispatch = useAppDispatch();
  const project = state.activeProjectId ? state.projects[state.activeProjectId] : null;

  return (
    <div className="sidebar">
      <ProjectSwitcher />
      {project ? (
        <>
          <div className="sidebar-section-tabs">
            <button
              className={`sidebar-section-tab${state.ui.sidebarSection === "files" ? " active" : ""}`}
              onClick={() => dispatch({ type: "SIDEBAR_SECTION", section: "files" })}
            >
              Files
            </button>
            <button
              className={`sidebar-section-tab${state.ui.sidebarSection === "runs" ? " active" : ""}`}
              onClick={() => dispatch({ type: "SIDEBAR_SECTION", section: "runs" })}
            >
              Runs
            </button>
          </div>
          <div className="sidebar-section">
            {state.ui.sidebarSection === "files" ? <FileTree project={project} /> : <RunList project={project} />}
          </div>
        </>
      ) : (
        <div className="sidebar-empty">No project open. Use the switcher above to create or open one.</div>
      )}
    </div>
  );
}
