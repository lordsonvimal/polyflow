import { GraphNode, GraphEdge } from "../../lib/types";
import { applyFileGrouping } from "../../lib/filegroup";

export const BUDGET = 1500;

export interface NarrowingChild {
  key: string;
  label: string;
  count: number;
}

export interface BudgetOk {
  ok: true;
  nodeCount: number;
  edgeCount: number;
}

export interface BudgetOver {
  ok: false;
  nodeCount: number;
  edgeCount: number;
  total: number;
  children: NarrowingChild[];
}

export type BudgetResult = BudgetOk | BudgetOver;

export function checkBudget(
  nodes: GraphNode[],
  edges: GraphEdge[],
  groupKey: (n: GraphNode) => string = (n) => n.service,
): BudgetResult {
  const total = nodes.length + edges.length;
  if (total <= BUDGET) return { ok: true, nodeCount: nodes.length, edgeCount: edges.length };
  const counts = new Map<string, number>();
  for (const n of nodes) {
    const k = groupKey(n);
    if (k) counts.set(k, (counts.get(k) ?? 0) + 1);
  }
  const children = [...counts.entries()]
    .map(([key, count]) => ({ key, label: key, count }))
    .sort((a, b) => a.count - b.count);
  return { ok: false, nodeCount: nodes.length, edgeCount: edges.length, total, children };
}

// autoCluster collapses file groups largest-first until the total is under budget.
export function autoCluster(
  nodes: GraphNode[],
  edges: GraphEdge[],
): { nodes: GraphNode[]; edges: GraphEdge[] } {
  const { groups } = applyFileGrouping(nodes, edges, []);
  const sorted = [...groups].sort((a, b) => b.members.length - a.members.length);
  const collapsed: string[] = [];
  let cur = { nodes, edges };
  for (const g of sorted) {
    collapsed.push(g.id);
    const r = applyFileGrouping(nodes, edges, collapsed);
    cur = { nodes: r.nodes, edges: r.edges };
    if (cur.nodes.length + cur.edges.length <= BUDGET) break;
  }
  return cur;
}

// layoutOptions derives layout config. Pure so it's testable without a DOM.
export function layoutOptions(
  hasCompounds: boolean,
  preferredLayout: string,
  reducedMotion: boolean,
): { name: string; animate: boolean; animationDuration: number; dagreDisabledReason?: string } {
  const canDagre = preferredLayout === "dagre" && !hasCompounds;
  return {
    name: canDagre ? "dagre" : "fcose",
    animate: !reducedMotion,
    animationDuration: 200,
    ...(preferredLayout === "dagre" && hasCompounds
      ? { dagreDisabledReason: "Dagre does not support compound (grouped) nodes" }
      : {}),
  };
}
