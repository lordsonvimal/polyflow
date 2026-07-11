// The visible-graph pipeline: raw store data → confidence filter →
// type/service filters → altitude transform (boundary collapse in in-depth
// mode, service aggregation in high-level mode). Graph.tsx renders exactly
// this; Filters/Detail derive their option lists from the same memos so the
// whole UI agrees on what is visible.

import { createMemo, createEffect } from "solid-js";
import { graphStore } from "./graph";
import { uiStore } from "./ui";
import { filterEdgesByConfidence } from "../lib/confidence";
import { applyBoundaryCollapse, BoundaryGroup } from "../lib/boundary";
import { applyFileGrouping, FileGroup } from "../lib/filegroup";
import { buildStructureView } from "../lib/structure";
import { aggregateServices } from "../lib/aggregate";
import { GraphNode, GraphEdge } from "../lib/types";

// Option lists for the filter panel, derived from the raw (unfiltered) data.
export const allNodeTypes = createMemo<string[]>(() => {
  const set = new Set<string>();
  for (const n of graphStore.nodes()) set.add(n.type);
  return [...set].sort();
});

export const allServices = createMemo<string[]>(() => {
  const set = new Set<string>();
  for (const n of graphStore.nodes()) set.add(n.service);
  return [...set].sort();
});

// Nodes/edges after confidence + type/service filters, before altitude
// transforms. Edges keep only endpoints that survived node filtering.
const filtered = createMemo<{ nodes: GraphNode[]; edges: GraphEdge[] }>(() => {
  const hiddenTypes = uiStore.hiddenTypes();
  const hiddenServices = uiStore.hiddenServices();
  // Variables are hidden unless the user opts in — except in structure view,
  // whose whole purpose is data flow, so it always keeps them.
  const hideVars = !uiStore.showVariables() && uiStore.viewMode() !== "structure";

  const nodes = graphStore
    .nodes()
    .filter(
      (n) =>
        !hiddenTypes.includes(n.type) &&
        !hiddenServices.includes(n.service) &&
        !(hideVars && n.type === "variable")
    );
  const nodeIds = new Set(nodes.map((n) => n.id));

  const edges = filterEdgesByConfidence(graphStore.edges(), uiStore.activeConfidence()).filter(
    (e) => nodeIds.has(e.from) && nodeIds.has(e.to)
  );
  return { nodes, edges };
});

// Boundary groups present in the current filtered graph (both collapsed and
// expanded) — the Detail panel uses this for the expand/collapse toggle.
export const boundaryGroups = createMemo<BoundaryGroup[]>(() => {
  const f = filtered();
  return applyBoundaryCollapse(f.nodes, f.edges, uiStore.expandedBoundaries()).groups;
});

// File groups present in the current in-depth graph — the Detail panel uses
// this for the per-file panel (copy path, impact, collapse toggle).
export const fileGroups = createMemo<FileGroup[]>(() => {
  const f = filtered();
  if (!uiStore.groupByFile() || uiStore.viewMode() === "highlevel") return [];
  const collapsed = applyBoundaryCollapse(f.nodes, f.edges, uiStore.expandedBoundaries());
  return applyFileGrouping(collapsed.nodes, collapsed.edges, uiStore.collapsedFiles()).groups;
});

// When variables become hidden (toggle off, or leaving the structure view),
// a variable that was selected is no longer on the canvas — drop the stale
// selection so the Detail panel doesn't point at an invisible node.
createEffect(() => {
  const hideVars = !uiStore.showVariables() && uiStore.viewMode() !== "structure";
  if (!hideVars) return;
  const id = uiStore.selectedNodeId();
  if (!id) return;
  const node = graphStore.nodes().find((n) => n.id === id);
  if (node?.type === "variable") uiStore.setSelectedNodeId(null);
});

export const visibleGraph = createMemo<{ nodes: GraphNode[]; edges: GraphEdge[] }>(() => {
  const f = filtered();
  if (uiStore.viewMode() === "highlevel") {
    return aggregateServices(f.nodes, f.edges);
  }
  if (uiStore.viewMode() === "structure") {
    const s = buildStructureView(f.nodes, f.edges);
    if (!uiStore.groupByFile()) return s;
    const grouped = applyFileGrouping(s.nodes, s.edges, uiStore.collapsedFiles());
    return { nodes: grouped.nodes, edges: grouped.edges };
  }
  const collapsed = applyBoundaryCollapse(f.nodes, f.edges, uiStore.expandedBoundaries());
  if (!uiStore.groupByFile()) {
    return { nodes: collapsed.nodes, edges: collapsed.edges };
  }
  const grouped = applyFileGrouping(collapsed.nodes, collapsed.edges, uiStore.collapsedFiles());
  return { nodes: grouped.nodes, edges: grouped.edges };
});
