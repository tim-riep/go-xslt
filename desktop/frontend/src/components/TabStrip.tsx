import { FILE_KIND_LABEL, RUN_KIND_BADGE } from "../lib/labels";
import { buffers, useBuffer } from "../state/buffers";
import { useContextMenu } from "../state/overlays";
import { useProjectActions } from "../state/projectActions";
import { useAppDispatch, useAppState, useOrderedTabs } from "../state/store";
import type { ContextMenuItem, Tab } from "../state/types";
import { Icon } from "./Icon";

export function TabStrip() {
  const tabs = useOrderedTabs();

  if (tabs.length === 0) {
    return (
      <div className="tab-strip">
        <div className="tab-strip-empty">No tabs open — pick a file or a run from the sidebar</div>
      </div>
    );
  }

  return (
    <div className="tab-strip">
      {tabs.map((tab) => (
        <TabItem key={tab.id} tab={tab} />
      ))}
    </div>
  );
}

function TabItem({ tab }: { tab: Tab }) {
  const state = useAppState();
  const dispatch = useAppDispatch();
  const actions = useProjectActions();
  const openMenu = useContextMenu();
  const buf = useBuffer(tab.kind === "file" ? tab.projectId : null, tab.kind === "file" ? tab.path : null);

  const active = tab.id === state.activeTabId;
  const dirty = tab.kind === "file" && buf.version !== buf.savedVersion;
  const project = state.projects[tab.projectId];
  const run = tab.kind === "run" ? project?.manifest.runs?.find((r) => r.id === tab.runId) : null;

  const name = tab.kind === "file" ? tab.path.split("/").pop() || tab.path : (run?.name ?? "Untitled run");
  const badge =
    tab.kind === "file" ? FILE_KIND_LABEL[project?.nodes[tab.path]?.kind ?? "other"] : RUN_KIND_BADGE[run?.kind ?? "transform"];
  const kindClass = tab.kind === "file" ? `kind-${project?.nodes[tab.path]?.kind ?? "other"}` : `kind-${run?.kind ?? "transform"}`;

  const menu: ContextMenuItem[] = [
    { id: "close", label: "Close", onSelect: () => actions.closeTab(tab) },
    { id: "close-others", label: "Close Others", onSelect: () => actions.closeOtherTabs(tab.id) },
    { id: "close-all", label: "Close All", onSelect: () => actions.closeAllTabs() },
    {
      id: "pin",
      label: tab.pinned ? "Unpin Tab" : "Pin Tab",
      separatorBefore: true,
      onSelect: () => dispatch({ type: "TAB_PIN", id: tab.id, pinned: !tab.pinned }),
    },
    ...(tab.kind === "file"
      ? [{ id: "reveal", label: "Reveal in Finder", onSelect: () => void actions.revealNode(tab.projectId, tab.path) }]
      : []),
  ];

  const classes = ["tab-strip-item"];
  if (active) classes.push("active");
  if (state.previewTabId === tab.id) classes.push("preview");
  if (dirty) classes.push("dirty");
  if (tab.kind === "file" && tab.missing) classes.push("missing");

  return (
    <div
      className={classes.join(" ")}
      onClick={() => dispatch({ type: "TAB_ACTIVATE", id: tab.id })}
      onDoubleClick={() => dispatch({ type: "TAB_PROMOTE", id: tab.id })}
      onMouseDown={(e) => {
        if (e.button === 1) {
          // middle-click closes, matching every other tabbed editor
          e.preventDefault();
          actions.closeTab(tab);
        }
      }}
      onContextMenu={(e) => openMenu(e, menu)}
      title={tab.kind === "file" ? tab.path : run?.name}
    >
      <span className={`tab-strip-item-icon ${kindClass}`}>{badge}</span>
      <span className="tab-strip-item-name">{name}</span>
      <span className="tab-strip-item-dirty-dot" />
      <span
        className="tab-strip-item-close"
        onClick={(e) => {
          e.stopPropagation();
          if (tab.kind === "file") void buffers.flush(tab.projectId, tab.path);
          actions.closeTab(tab);
        }}
      >
        <Icon name="close" size={10} />
      </span>
    </div>
  );
}
