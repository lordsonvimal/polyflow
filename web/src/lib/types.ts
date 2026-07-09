// Shared graph types mirrored from the server's Cytoscape JSON payload.

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
  confidence?: string;
  meta?: Record<string, string>;
}
