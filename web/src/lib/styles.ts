// Single source of truth for graph visuals. The Cytoscape stylesheet
// (Graph.tsx) and the Legend component are both generated from these tables,
// so the legend can never drift from what is actually drawn.

import { BOUNDARY_GROUP_TYPE } from "./boundary";
import { SERVICE_NODE_TYPE } from "./aggregate";
import { FILE_GROUP_TYPE } from "./filegroup";

export const CANVAS_BG = "#030712";
export const DEFAULT_NODE_COLOR = "#6b7280";
// Labels render below the node on the canvas background, so they must stay
// light regardless of the node's own fill color.
export const LABEL_COLOR = "#f9fafb";

export const LANG_COLORS: [lang: string, color: string][] = [
  ["go", "#00ADD8"],
  ["javascript", "#F7DF1E"],
  ["typescript", "#3178C6"],
  ["ruby", "#CC342D"],
  ["templ", "#7C3AED"],
];

export interface NodeTypeStyle {
  type: string;
  shape: string; // cytoscape shape name
  color?: string; // fixed fill; absent = language color (or default gray)
  glyph: string; // legend glyph approximating the shape
  desc: string; // legend description
}

// Order matters for the legend; every node type the backend can emit (plus
// the two client-side synthetic types) must appear here.
export const NODE_TYPE_STYLES: NodeTypeStyle[] = [
  { type: "function", shape: "ellipse", glyph: "●", desc: "function (language color)" },
  { type: "method", shape: "ellipse", glyph: "●", desc: "method (language color)" },
  { type: "component", shape: "ellipse", glyph: "●", desc: "UI component" },
  { type: "http_handler", shape: "round-rectangle", glyph: "◼", desc: "HTTP handler / route" },
  { type: "http_client", shape: "round-tag", glyph: "◗", desc: "HTTP/SSE/WS client call" },
  { type: "route", shape: "round-rectangle", glyph: "◼", desc: "declared route" },
  { type: "channel", shape: "diamond", color: "#f59e0b", glyph: "◆", desc: "broker channel / exchange" },
  { type: "publisher", shape: "vee", glyph: "▼", desc: "message publisher / job enqueue" },
  { type: "subscriber", shape: "rhomboid", glyph: "▱", desc: "message subscriber / handler" },
  { type: "worker", shape: "ellipse", glyph: "●", desc: "background worker / goroutine" },
  { type: "datastore", shape: "barrel", color: "#10b981", glyph: "▮", desc: "datastore (DB)" },
  { type: "external_service", shape: "hexagon", color: "#ec4899", glyph: "⬡", desc: "external service (cloud SDK)" },
  { type: "dom_target", shape: "ellipse", glyph: "●", desc: "DOM element access" },
  { type: "templ_element", shape: "ellipse", glyph: "●", desc: "templ template element" },
  { type: "variable", shape: "tag", color: "#f97316", glyph: "◖", desc: "tracked variable (global / captured)" },
  { type: "struct", shape: "round-rectangle", color: "#0ea5e9", glyph: "▭", desc: "struct (fields in detail panel)" },
  { type: "class", shape: "round-rectangle", color: "#8b5cf6", glyph: "▭", desc: "class (methods/fields in detail panel)" },
  {
    type: FILE_GROUP_TYPE,
    shape: "round-rectangle",
    color: "#111827",
    glyph: "▢",
    desc: "file container (double-click to collapse/expand)",
  },
  {
    type: BOUNDARY_GROUP_TYPE,
    shape: "round-rectangle",
    color: "#312e81",
    glyph: "▢",
    desc: "framework/SDK boundary group (collapsed — double-click to expand)",
  },
  {
    type: SERVICE_NODE_TYPE,
    shape: "round-rectangle",
    color: "#4f46e5",
    glyph: "▣",
    desc: "service (high-level view)",
  },
];

export interface EdgeLegendEntry {
  glyph: string;
  color?: string;
  desc: string;
}

export const EDGE_LEGEND: EdgeLegendEntry[] = [
  { glyph: "──▶", desc: "static / inferred edge (calls, http, renders, publishes…)" },
  { glyph: "──▶", color: "#ef4444", desc: "writes / mutates variable" },
  { glyph: "──▶", color: "#64748b", desc: "reads variable" },
  { glyph: "┄┄▶", color: "#f97316", desc: "captures (closure over variable)" },
  { glyph: "──▶", color: "#22d3ee", desc: "flows_to (variable passed by ref/value)" },
  { glyph: "┈┈▶", color: "#a855f7", desc: "event binding (onClick, oninput…) — labeled with the event" },
  { glyph: "┄┄▶", desc: "partial / unknown edge (opt-in, dashed)" },
  { glyph: "◯", color: "#f472b6", desc: "trace root (pink ring)" },
  { glyph: "◻", color: "#ffffff", desc: "selected node (white border, neighbors highlighted)" },
];
