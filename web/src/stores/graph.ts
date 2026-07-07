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

const [nodes, setNodes] = createSignal<GraphNode[]>([]);
const [edges, setEdges] = createSignal<GraphEdge[]>([]);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);

async function fetchGraph(query: string) {
  setLoading(true);
  setError(null);
  try {
    const res = await fetch(`/api/search?q=${encodeURIComponent(query)}&limit=100`);
    if (!res.ok) throw new Error(`API error: ${res.status}`);
    const data: GraphNode[] = await res.json();
    setNodes(data);
  } catch (e) {
    setError(String(e));
  } finally {
    setLoading(false);
  }
}

export const graphStore = { nodes, edges, loading, error, fetchGraph, setNodes, setEdges };
