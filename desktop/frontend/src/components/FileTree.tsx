import { useMemo, type MouseEvent as ReactMouseEvent } from "react";
import type { FileKind } from "../lib/api";
import { FILE_KIND_LABEL } from "../lib/labels";
import { useContextMenu } from "../state/overlays";
import { useProjectActions } from "../state/projectActions";
import { dirname } from "../state/paths";
import { useT } from "../i18n";
import { shellMessages } from "../i18n/messages/shell";
import { useAppState } from "../state/store";
import type { ProjectState } from "../state/types";
import { Icon } from "./Icon";

interface VisibleRow {
  path: string;
  name: string;
  dir: boolean;
  kind?: FileKind;
  depth: number;
}

function buildVisibleRows(project: ProjectState): VisibleRow[] {
  const rows: VisibleRow[] = [];
  const walk = (paths: string[], depth: number) => {
    for (const p of paths) {
      const node = project.nodes[p];
      if (!node) continue;
      rows.push({ path: node.path, name: node.name, dir: node.dir, kind: node.kind, depth });
      if (node.dir && project.expanded[p]) walk(node.children ?? [], depth + 1);
    }
  };
  walk(project.rootChildren, 0);
  return rows;
}

export function FileTree({ project }: { project: ProjectState }) {
  const state = useAppState();
  const actions = useProjectActions();
  const openMenu = useContextMenu();
  const t = useT(shellMessages);
  const rows = useMemo(() => buildVisibleRows(project), [project]);

  const activeTab = state.activeTabId ? state.tabs[state.activeTabId] : null;
  const selectedPath = activeTab?.kind === "file" && activeTab.projectId === project.id ? activeTab.path : null;

  const newFileAt = async (dir: string) => {
    const name = await actions.prompt({ title: t("newFileTitle"), label: t("nameLabel"), placeholder: "template.xsl" });
    if (name && !name.includes("/")) await actions.newFile(project.id, dir, name);
  };
  const newFolderAt = async (dir: string) => {
    const name = await actions.prompt({ title: t("newFolderTitle"), label: t("nameLabel"), placeholder: "lib" });
    if (name && !name.includes("/")) await actions.newFolder(project.id, dir, name);
  };

  const menuForRow = (row: VisibleRow) => [
    ...(row.dir
      ? [
          { id: "new-file", label: t("newFileMenu"), onSelect: () => void newFileAt(row.path) },
          { id: "new-folder", label: t("newFolderMenu"), onSelect: () => void newFolderAt(row.path) },
        ]
      : []),
    {
      id: "rename",
      label: t("renameMenu"),
      separatorBefore: row.dir,
      onSelect: async () => {
        const name = await actions.prompt({ title: t("renameTitle"), initialValue: row.name });
        if (name && name !== row.name && !name.includes("/")) {
          const parent = dirname(row.path);
          await actions.renameNode(project.id, row.path, parent ? `${parent}/${name}` : name);
        }
      },
    },
    ...(row.dir ? [] : [{ id: "duplicate", label: t("duplicate"), onSelect: () => void actions.duplicateNode(project.id, row.path) }]),
    {
      id: "delete",
      label: t("deleteMenu"),
      danger: true,
      onSelect: () => void actions.confirmDeleteNode(project.id, row.path, row.dir),
    },
    { id: "reveal", label: t("reveal"), separatorBefore: true, onSelect: () => void actions.revealNode(project.id, row.path) },
  ];

  return (
    <div
      className="file-tree"
      onContextMenu={(e: ReactMouseEvent) => {
        if (e.target !== e.currentTarget) return;
        openMenu(e, [
          { id: "new-file", label: t("newFileMenu"), onSelect: () => void newFileAt("") },
          { id: "new-folder", label: t("newFolderMenu"), onSelect: () => void newFolderAt("") },
        ]);
      }}
    >
      {rows.length === 0 && <div className="sidebar-empty">{t("noFiles")}</div>}
      {rows.map((row) => (
        <div
          key={row.path}
          className={`tree-row${selectedPath === row.path ? " selected" : ""}${row.dir ? "" : ""}`}
          style={{ paddingInlineStart: 6 + row.depth * 14 }}
          onClick={() => (row.dir ? actions.toggleNode(project.id, row.path) : void actions.openFile(project.id, row.path, { preview: true }))}
          onDoubleClick={() => !row.dir && void actions.openFile(project.id, row.path)}
          onContextMenu={(e) => openMenu(e, menuForRow(row))}
        >
          <Icon
            name="chevron-right"
            className={`tree-row-caret${row.dir ? (project.expanded[row.path] ? "" : " collapsed") : " leaf"}`}
          />
          {row.dir ? (
            <Icon name="folder" className="tree-row-icon dir" />
          ) : (
            <span className={`tree-row-icon kind-${row.kind ?? "other"}`}>{FILE_KIND_LABEL[row.kind ?? "other"]}</span>
          )}
          <span className="tree-row-name">{row.name}</span>
        </div>
      ))}
    </div>
  );
}
