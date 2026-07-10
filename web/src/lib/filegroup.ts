// File grouping: every node that carries a file path is wrapped in a
// compound parent node per (service, file), so the graph reads as
// service ▸ file ▸ nodes. Runs after boundary collapse in the visible-graph
// pipeline — collapsed boundary groups have no file and stay top-level.
// Individual files can additionally be collapsed to a single node, with
// edges re-routed and deduplicated (same rules as boundary collapse).

import { GraphNode, GraphEdge } from "./types";

export const FILE_GROUP_TYPE = "file_group";
const GROUP_PREFIX = "filegrp:";

export function isFileGroupId(id: string): boolean {
  return id.startsWith(GROUP_PREFIX);
}

export function fileGroupId(service: string, file: string): string {
  return `${GROUP_PREFIX}${service}:${file}`;
}

export function fileBasename(path: string): string {
  return path.split("/").pop() || path;
}

export interface FileGroup {
  id: string;
  service: string;
  file: string;
  members: GraphNode[];
}

export interface FileGroupResult {
  nodes: GraphNode[];
  edges: GraphEdge[];
  groups: FileGroup[];
}

// applyFileGrouping wraps file-bearing nodes in compound parents. Files
// listed in collapsedFiles (by group id) are folded into one node instead:
// members disappear, edges re-route to the group node and deduplicate by
// (source, target, type), and edges entirely inside one file vanish.
export function applyFileGrouping(
  nodes: GraphNode[],
  edges: GraphEdge[],
  collapsedFiles: readonly string[]
): FileGroupResult {
  const groups = new Map<string, FileGroup>();
  for (const n of nodes) {
    if (!n.file || n.type === FILE_GROUP_TYPE) continue;
    const id = fileGroupId(n.service, n.file);
    let g = groups.get(id);
    if (!g) {
      g = { id, service: n.service, file: n.file, members: [] };
      groups.set(id, g);
    }
    g.members.push(n);
  }

  const memberToGroup = new Map<string, string>();
  for (const g of groups.values()) {
    if (!collapsedFiles.includes(g.id)) continue;
    for (const m of g.members) memberToGroup.set(m.id, g.id);
  }

  const existingGroupIds = new Set(
    nodes.filter((n) => n.type === FILE_GROUP_TYPE).map((n) => n.id)
  );

  const outNodes: GraphNode[] = [];
  for (const n of nodes) {
    if (memberToGroup.has(n.id)) continue;
    const g = n.file && n.type !== FILE_GROUP_TYPE ? groups.get(fileGroupId(n.service, n.file)) : undefined;
    if (g && !collapsedFiles.includes(g.id)) {
      outNodes.push({ ...n, parent: g.id });
    } else {
      outNodes.push(n);
    }
  }
  for (const g of groups.values()) {
    if (existingGroupIds.has(g.id)) continue;
    const collapsed = collapsedFiles.includes(g.id);
    outNodes.push({
      id: g.id,
      type: FILE_GROUP_TYPE,
      label: collapsed ? `${fileBasename(g.file)} (${g.members.length})` : fileBasename(g.file),
      service: g.service,
      file: g.file,
      line: 0,
      language: g.members[0]?.language ?? "",
      meta: {
        member_count: String(g.members.length),
        ...(collapsed ? { collapsed: "true" } : {}),
      },
    });
  }

  const outEdges: GraphEdge[] = [];
  const seen = new Set<string>();
  for (const e of edges) {
    const from = memberToGroup.get(e.from) ?? e.from;
    const to = memberToGroup.get(e.to) ?? e.to;
    if (from === to && (memberToGroup.has(e.from) || memberToGroup.has(e.to))) {
      continue; // edge entirely inside one collapsed file
    }
    const rerouted = from !== e.from || to !== e.to;
    if (rerouted) {
      const dedupeKey = `${from} ${to} ${e.type}`;
      if (seen.has(dedupeKey)) continue;
      seen.add(dedupeKey);
      outEdges.push({ ...e, id: `fg:${e.id}`, from, to });
    } else {
      outEdges.push(e);
    }
  }

  return { nodes: outNodes, edges: outEdges, groups: [...groups.values()] };
}
