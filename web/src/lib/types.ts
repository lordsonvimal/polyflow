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
  // Compound-node containment (client-side only): id of the parent group
  // node, set by the file-grouping transform.
  parent?: string;
}

export interface GraphEdge {
  id: string;
  from: string;
  to: string;
  type: string;
  label?: string;
  confidence?: string;
  // UF.6: plan-10's edge-styling contract, now on every general-purpose
  // graph endpoint (see internal/server/cytoscape.go's CytoscapeEdgeData).
  verificationState?: string;
  meta?: Record<string, string>;
}
