import { createSignal } from "solid-js";

export interface GraphNode {
  id: string;
  type: string;
  label: string;
  service: string;
  file: string;
  line: number;
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
  direction: "forward" | "backward" | "both" = "forward",
  depth = 10
) {
  setLoading(true);
  setError(null);
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

export const graphStore = {
  nodes,
  edges,
  loading,
  error,
  fetchGraph,
  fetchTrace,
  setNodes,
  setEdges,
};
