import { createSignal, createEffect } from "solid-js";

export interface GraphNode {
  id: string;
  type: string;
  label: string;
  service: string;
  file: string;
  line: number;
  language: string;
  meta?: Record<string, string>;
}

export interface GraphEdge {
  id: string;
  from: string;
  to: string;
  type: string;
  label?: string;
}

interface CytoscapeNodeData {
  id: string;
  label: string;
  type: string;
  service: string;
  file: string;
  line: number;
  language: string;
}

interface CytoscapeEdgeData {
  id: string;
  source: string;
  target: string;
  type: string;
  label?: string;
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
  (urlParam("direction") as TraceDirection | null) ?? "forward"
);
const [traceDepth, setTraceDepth] = createSignal<number>(
  parseInt(urlParam("depth") ?? "10", 10)
);

// ── Graph data ─────────────────────────────────────────────────────────────

const [nodes, setNodes] = createSignal<GraphNode[]>([]);
const [edges, setEdges] = createSignal<GraphEdge[]>([]);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);

function cytoscapeToStore(g: CytoscapeGraph) {
  setNodes(
    g.nodes.map((n) => ({
      id: n.data.id,
      type: n.data.type,
      label: n.data.label,
      service: n.data.service,
      file: n.data.file,
      line: n.data.line,
      language: n.data.language ?? "",
    }))
  );
  setEdges(
    g.edges.map((e) => ({
      id: e.data.id,
      from: e.data.source,
      to: e.data.target,
      type: e.data.type,
      label: e.data.label,
    }))
  );
}

async function fetchGraph(page = 1, limit = 500) {
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

async function fetchTrace(
  rootId: string,
  direction: TraceDirection = "forward",
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
  fetchGraph,
  fetchTrace,
  traceRoot,
  traceDirection,
  traceDepth,
  setNodes,
  setEdges,
};
