import { createSignal, createEffect } from "solid-js";
import { GraphNode, GraphEdge } from "../lib/types";

export type { GraphNode, GraphEdge };

interface CytoscapeNodeData {
  id: string;
  label: string;
  type: string;
  service: string;
  file: string;
  line: number;
  language: string;
  meta?: Record<string, string>;
}

interface CytoscapeEdgeData {
  id: string;
  source: string;
  target: string;
  type: string;
  label?: string;
  confidence?: string;
  meta?: Record<string, string>;
}

interface CytoscapeGraph {
  nodes: { data: CytoscapeNodeData }[];
  edges: { data: CytoscapeEdgeData }[];
}

// ── URL helpers (shared subset — full impl lives in ui.ts) ─────────────────

function urlParam(key: string): string | null {
  return new URLSearchParams(window.location.search).get(key);
}

function pushURLGraphParams(p: {
  root?: string | null;
  direction?: string | null;
  depth?: number | null;
}) {
  const sp = new URLSearchParams(window.location.search);
  const set = (k: string, v: string | null | undefined) => {
    if (v === null || v === undefined || v === "") sp.delete(k);
    else sp.set(k, v);
  };
  set("root", p.root);
  set("direction", p.direction);
  set("depth", p.depth != null ? String(p.depth) : null);
  const query = sp.toString();
  window.history.pushState(null, "", query ? `?${query}` : window.location.pathname);
}

// ── Trace state (kept here so Graph.tsx can restore from URL on mount) ──────

export type TraceDirection = "forward" | "backward" | "both";

const [traceRoot, setTraceRoot] = createSignal<string | null>(urlParam("root"));
const [traceDirection, setTraceDirection] = createSignal<TraceDirection>(
  (urlParam("direction") as TraceDirection | null) ?? "both"
);
const [traceDepth, setTraceDepth] = createSignal<number>(
  parseInt(urlParam("depth") ?? "10", 10)
);

// ── Graph data ─────────────────────────────────────────────────────────────

const [nodes, setNodes] = createSignal<GraphNode[]>([]);
const [edges, setEdges] = createSignal<GraphEdge[]>([]);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);
const [stats, setStats] = createSignal<{ nodes: number; edges: number } | null>(null);

function cytoscapeToStore(g: CytoscapeGraph) {
  setNodes(
    (g.nodes ?? []).map((n) => ({
      id: n.data.id,
      type: n.data.type,
      label: n.data.label,
      service: n.data.service,
      file: n.data.file,
      line: n.data.line,
      language: n.data.language ?? "",
      meta: n.data.meta,
    }))
  );
  setEdges(
    (g.edges ?? []).map((e) => ({
      id: e.data.id,
      from: e.data.source,
      to: e.data.target,
      type: e.data.type,
      label: e.data.label,
      confidence: e.data.confidence,
      meta: e.data.meta,
    }))
  );
}

async function fetchGraph(page = 1, limit = 2000) {
  setLoading(true);
  setError(null);
  try {
    const res = await fetch(`/api/graph?page=${page}&limit=${limit}`);
    if (!res.ok) throw new Error(`API error: ${res.status}`);
    const data: CytoscapeGraph = await res.json();
    cytoscapeToStore(data);
  } catch (e) {
    setError(String(e));
  } finally {
    setLoading(false);
  }
}

async function fetchStats() {
  try {
    const res = await fetch("/api/stats");
    if (!res.ok) return;
    const data = await res.json();
    setStats({ nodes: data.nodes ?? 0, edges: data.edges ?? 0 });
  } catch {
    // ignore — stats are cosmetic
  }
}

async function fetchTrace(
  rootId: string,
  direction: TraceDirection = "both",
  depth = 10
) {
  setLoading(true);
  setError(null);
  setTraceRoot(rootId);
  setTraceDirection(direction);
  setTraceDepth(depth);
  pushURLGraphParams({ root: rootId, direction, depth });
  try {
    const res = await fetch(
      `/api/graph/trace?root=${encodeURIComponent(rootId)}&direction=${direction}&depth=${depth}`
    );
    if (!res.ok) throw new Error(`API error: ${res.status}`);
    const data: CytoscapeGraph = await res.json();
    cytoscapeToStore(data);
  } catch (e) {
    setError(String(e));
  } finally {
    setLoading(false);
  }
}

// clearTrace returns to the full-graph view.
function clearTrace() {
  setTraceRoot(null);
  pushURLGraphParams({ root: null, direction: null, depth: null });
  fetchGraph();
}

// retrace re-runs the active trace with a new direction/depth.
function retrace(direction?: TraceDirection, depth?: number) {
  const root = traceRoot();
  if (!root) return;
  fetchTrace(root, direction ?? traceDirection(), depth ?? traceDepth());
}

// Restore trace from URL on startup.
createEffect(() => {
  const root = traceRoot();
  if (root) {
    fetchTrace(root, traceDirection(), traceDepth());
  }
});

export const graphStore = {
  nodes,
  edges,
  loading,
  error,
  stats,
  fetchGraph,
  fetchStats,
  fetchTrace,
  clearTrace,
  retrace,
  traceRoot,
  traceDirection,
  traceDepth,
  setNodes,
  setEdges,
};
