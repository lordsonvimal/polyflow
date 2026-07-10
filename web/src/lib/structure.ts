// Structure (flow-diagram) view: a UML-ish projection of the graph showing
// classes/structs/interfaces with their fields, tracked variables, and the
// functions that read/write/capture them. Communication edges (http, brokers,
// DOM) are omitted — this view answers "how is the data shaped and who
// touches it", not "what talks to what over the wire".

import { GraphNode, GraphEdge } from "./types";

const STRUCTURE_NODE_TYPES = new Set([
  "struct",
  "class",
  "interface",
  "type_alias",
  "variable",
  "function",
  "method",
  "component",
]);

const STRUCTURE_EDGE_TYPES = new Set([
  "declares",
  "reads",
  "writes",
  "captures",
  "flows_to",
  "uses_type",
  "calls",
]);

export const MAX_LABEL_FIELDS = 6;

// structLabel renders "Name" plus up to MAX_LABEL_FIELDS field lines for
// struct/class nodes; other nodes keep their plain label.
export function structLabel(n: GraphNode): string {
  let fields: string[] = [];
  if (n.type === "struct" && n.meta?.fields) {
    try {
      const parsed = JSON.parse(n.meta.fields) as { name: string; type: string }[];
      fields = parsed.map((f) => `${f.name}: ${shortType(f.type)}`);
    } catch {
      /* malformed meta — plain label */
    }
  } else if (n.type === "class") {
    const props = (n.meta?.fields || n.meta?.attrs || "").split(",").filter(Boolean);
    const methods = (n.meta?.methods || "").split(",").filter(Boolean);
    fields = [...props, ...methods.map((m) => `${m}()`)];
  } else if (n.type === "variable" && n.meta?.data_type) {
    return `${n.label}: ${shortType(n.meta.data_type)}`;
  }
  if (fields.length === 0) return n.label;
  const shown = fields.slice(0, MAX_LABEL_FIELDS);
  if (fields.length > MAX_LABEL_FIELDS) shown.push(`… ${fields.length - MAX_LABEL_FIELDS} more`);
  return [n.label, "———", ...shown].join("\n");
}

// shortType strips package qualifiers so labels stay compact:
// "github.com/x/y.User" → "y.User", "map[string]*pkg.T" stays readable.
export function shortType(t: string): string {
  return t.replace(/[\w./-]*\//g, "");
}

export function buildStructureView(
  nodes: GraphNode[],
  edges: GraphEdge[]
): { nodes: GraphNode[]; edges: GraphEdge[] } {
  const kept = nodes.filter((n) => STRUCTURE_NODE_TYPES.has(n.type));
  const keptIds = new Set(kept.map((n) => n.id));
  const structEdges = edges.filter(
    (e) => STRUCTURE_EDGE_TYPES.has(e.type) && keptIds.has(e.from) && keptIds.has(e.to)
  );

  // Drop functions that have no structural relationship at all — they would
  // fill the view with disconnected dots.
  const connected = new Set<string>();
  for (const e of structEdges) {
    connected.add(e.from);
    connected.add(e.to);
  }
  const outNodes = kept
    .filter((n) => connected.has(n.id) || !["function", "method", "component"].includes(n.type))
    .map((n) => ({ ...n, label: structLabel(n) }));

  const outIds = new Set(outNodes.map((n) => n.id));
  return {
    nodes: outNodes,
    edges: structEdges.filter((e) => outIds.has(e.from) && outIds.has(e.to)),
  };
}
