// Project-relative path helpers, shared by the tree/tab/run reducers so
// "what does renaming or deleting a path affect" is answered in exactly one
// place. Paths are always POSIX-style (forward slash), never OS-native, and
// "" denotes the project root.
import type { FileNode } from "../lib/api";

export function dirname(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "" : path.slice(0, i);
}

export function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? path : path.slice(i + 1);
}

export function join(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name;
}

/** True if `path` is `ancestor` itself or nested under it. */
export function isSelfOrDescendant(path: string, ancestor: string): boolean {
  return path === ancestor || path.startsWith(`${ancestor}/`);
}

/** Rewrites `path` after `from` was renamed/moved to `to` — including when
 * `path` is a descendant of `from` (a folder rename must carry its
 * children's paths along). Returns `path` unchanged if unaffected. */
export function remapPath(path: string, from: string, to: string): string {
  if (path === from) return to;
  if (path.startsWith(`${from}/`)) return to + path.slice(from.length);
  return path;
}

// ---- tab identity ----

export type TabId = string;

export function fileTabId(projectId: string, path: string): TabId {
  return `file:${projectId}:${path}`;
}

export function runTabId(projectId: string, runId: string): TabId {
  return `run:${projectId}:${runId}`;
}

// ---- flat tree ----

export interface FlatNode {
  path: string;
  name: string;
  dir: boolean;
  kind?: FileNode["kind"];
  children?: string[]; // child paths, present (possibly empty) only for dir
}

/** Flattens a recursively-nested FileNode (as scanned by the backend) into a
 * path-keyed map, which every reducer here works against — an O(1) lookup by
 * path instead of a tree walk for every tab/run remap. */
export function flattenTree(root: FileNode): { nodes: Record<string, FlatNode>; rootChildren: string[] } {
  const nodes: Record<string, FlatNode> = {};

  function visit(n: FileNode): string[] | undefined {
    if (!n.dir) return undefined;
    const childPaths: string[] = [];
    for (const c of n.children ?? []) {
      if (!c) continue;
      const children = visit(c);
      nodes[c.path] = { path: c.path, name: c.name, dir: c.dir, kind: c.kind, children };
      childPaths.push(c.path);
    }
    return childPaths;
  }

  const rootChildren = visit(root) ?? [];
  return { nodes, rootChildren };
}
