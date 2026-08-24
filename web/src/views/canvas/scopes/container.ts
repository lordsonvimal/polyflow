// Shared engine behind service.ts (rootPath="") and folder.ts
// (rootPath=the folder's path): both scopes collapse their immediate
// /api/tree children into single summary nodes, aggregate the edges
// between them, and represent anything crossing the container's boundary —
// a sibling folder/file at the service root, or another service entirely —
// as a stub connector rather than expanding it (plan-10's boundary-stub
// contract).
import { GraphNode, GraphEdge } from "../../../lib/types";
import { apiFetchJSON } from "../../../lib/apiFetch";
import { ApiTreeNode, ApiTreeResult } from "../../../stores/tree";
import { serviceNodeId, isBridgeOnlyService } from "../../../lib/aggregate";
import { GraphData, fetchAllGraph, sortGraphData, stubNode } from "./common";

interface Group {
  id: string;
  kind: "folder" | "file";
  label: string;
  path: string;
  nodeCount: number;
}

function countLeaves(n: ApiTreeNode): number {
  if (n.kind === "file") return 1;
  return (n.children ?? []).reduce((s, c) => s + countLeaves(c), 0);
}

function groupsFromChildren(service: string, children: ApiTreeNode[]): Group[] {
  return children
    .filter((n) => n.kind === "folder" || n.kind === "file")
    .map((n): Group =>
      n.kind === "folder"
        ? { id: `folder:${service}:${n.path}`, kind: "folder", label: n.name, path: n.path ?? "", nodeCount: countLeaves(n) }
        : { id: n.node_id || `file:${service}:${n.path}`, kind: "file", label: n.name, path: n.path ?? "", nodeCount: 1 },
    )
    .sort((a, b) => a.id.localeCompare(b.id));
}

// Finds the subtree rooted at `rootPath` ("" = the service root).
function findSubtree(tree: ApiTreeNode[], rootPath: string): ApiTreeNode[] {
  if (rootPath === "") return tree;
  for (const n of tree) {
    if (n.kind !== "folder" || n.path === undefined) continue;
    if (n.path === rootPath) return n.children ?? [];
    if (rootPath.startsWith(n.path + "/")) return findSubtree(n.children ?? [], rootPath);
  }
  return [];
}

function groupForFile(groups: Group[], file: string): Group | undefined {
  return groups.find((g) => (g.kind === "file" ? g.path === file : file === g.path || file.startsWith(g.path + "/")));
}

export async function resolveContainer(
  service: string,
  rootPath: string,
  signal?: AbortSignal,
): Promise<GraphData> {
  const tree = await apiFetchJSON<ApiTreeResult>(`/api/tree?service=${encodeURIComponent(service)}`, { signal });
  const containerGroups = groupsFromChildren(service, findSubtree(tree.tree, rootPath));
  const rootGroups = groupsFromChildren(service, tree.tree);
  const containerIds = new Set(containerGroups.map((g) => g.id));

  const all = await fetchAllGraph(signal);
  const nodeById = new Map(all.nodes.map((n) => [n.id, n]));

  function groupIdFor(n: GraphNode): string | undefined {
    // Nodes with no service attribution (e.g. the contract engine's synthetic
    // "unresolved"/"unresolved:<svc>" bucket for unmatched HTTP calls) aren't
    // foreign-service members — serviceNodeId("") would wrongly group them
    // under a blank-labeled "service:" stub. Stand in for themselves instead.
    if (!n.service) return n.id;
    if (n.service !== service) return serviceNodeId(n.service);
    return (groupForFile(containerGroups, n.file) ?? groupForFile(rootGroups, n.file))?.id;
  }

  // UN.8: one edge per *unordered* group pair, not per (direction, type) —
  // same fix as lib/aggregate.ts's overview aggregation, for the same
  // reason: a pair with traffic both ways (e.g. a folder's http_client
  // calling out, and an sse push edge coming back the other way) drew as
  // two near-parallel/overlapping lines that were impossible to tell apart
  // and, worse, only left the topmost one clickable — the other's existence
  // was invisible. types accumulates every distinct edge type crossing the
  // pair so the merged edge's label stays informative.
  const agg = new Map<string, { a: string; b: string; total: number; types: Set<string>; forward: number; backward: number }>();
  const stubIds = new Set<string>();

  for (const e of all.edges) {
    const fromNode = nodeById.get(e.from);
    const toNode = nodeById.get(e.to);
    if (!fromNode || !toNode) continue;
    const fromGroup = groupIdFor(fromNode);
    const toGroup = groupIdFor(toNode);
    if (!fromGroup || !toGroup || fromGroup === toGroup) continue;
    const fromIn = containerIds.has(fromGroup);
    const toIn = containerIds.has(toGroup);
    if (!fromIn && !toIn) continue; // neither endpoint touches this container
    if (!fromIn) stubIds.add(fromGroup);
    if (!toIn) stubIds.add(toGroup);
    // Dedupe key is order-independent, but a/b (the drawn edge's own
    // from/to) keep whichever direction was observed *first* — a
    // unidirectional pair must keep its real arrow direction rather than
    // getting force-flipped to alphabetical order.
    const key = fromGroup < toGroup ? `${fromGroup} ${toGroup}` : `${toGroup} ${fromGroup}`;
    let g = agg.get(key);
    if (!g) {
      g = { a: fromGroup, b: toGroup, total: 0, types: new Set(), forward: 0, backward: 0 };
      agg.set(key, g);
    }
    g.total++;
    g.types.add(e.type);
    if (fromGroup === g.a) g.forward++;
    else g.backward++;
  }

  const nodes: GraphNode[] = containerGroups.map((g) => ({
    id: g.id,
    type: g.kind,
    label: g.label,
    service,
    file: g.kind === "file" ? g.path : "",
    line: 0,
    language: "",
    // Folder compounds carry no `file` (that field is reserved for an
    // actual file path) — CanvasHost's drill handler reads meta.path
    // instead when double-clicking one open into a folder scope.
    meta: { node_count: String(g.nodeCount), ...(g.kind === "folder" ? { path: g.path } : {}) },
  }));

  for (const id of stubIds) {
    if (id.startsWith("service:")) {
      const svc = id.slice("service:".length);
      nodes.push(stubNode(id, svc, svc, "service", "", undefined, isBridgeOnlyService(all.nodes, svc)));
      continue;
    }
    const rg = rootGroups.find((g) => g.id === id);
    if (rg) {
      nodes.push(stubNode(id, rg.label, service, rg.kind, rg.path, rg.nodeCount));
      continue;
    }
    // Not a folder/file group and not a real foreign service — a graph node
    // with no service attribution standing in for itself (see groupIdFor).
    const raw = nodeById.get(id);
    if (raw) nodes.push(stubNode(id, raw.label, raw.service, "service", ""));
  }

  const edges: GraphEdge[] = [...agg.values()].map((g) => {
    const bidirectional = g.forward > 0 && g.backward > 0;
    const types = [...g.types].sort();
    const label = types.length > 1 ? `${g.total} edges` : g.total > 1 ? `${types[0]} ×${g.total}` : types[0];
    return {
      id: `agg:${g.a}-${g.b}`,
      from: g.a,
      to: g.b,
      type: types.length === 1 ? types[0] : "cross_service",
      label,
      meta: { bidirectional: bidirectional ? "true" : "false" },
    };
  });

  return sortGraphData({ nodes, edges });
}
