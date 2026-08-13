// Pure element-set filtering for ViewState.filters (US.1 / UN.2). Kept
// side-effect-free so "filter chips -> element-set diff" is unit-testable
// without Cytoscape or a DOM.
import { GraphNode, GraphEdge } from "./types";
import { filterEdgesByConfidence, DEFAULT_CONFIDENCE } from "./confidence";
import { edgeGroupOf } from "./edgeGroups";

export interface FilterableGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface GraphFilters {
  confidence: string[];
  edgeTypes: string[]; // active edgeGroups.EDGE_GROUP_NAMES; [] = no restriction
  services: string[]; // active service names; [] = no restriction
}

// [] on edgeTypes/services means "no explicit restriction" (show
// everything). [] on confidence falls back to lib/confidence.ts's
// DEFAULT_CONFIDENCE (static+inferred) — partial/unknown stay opt-in even
// before any chip has ever been touched.
export function applyFilters(d: FilterableGraph, filters: GraphFilters): FilterableGraph {
  let nodes = d.nodes;
  if (filters.services.length > 0) {
    const activeServices = new Set(filters.services);
    nodes = nodes.filter((n) => activeServices.has(n.service));
  }
  const nodeIds = new Set(nodes.map((n) => n.id));

  const activeConfidence = filters.confidence.length > 0 ? filters.confidence : DEFAULT_CONFIDENCE;
  let edges = filterEdgesByConfidence(d.edges, activeConfidence);

  if (filters.edgeTypes.length > 0) {
    const activeGroups = new Set(filters.edgeTypes);
    edges = edges.filter((e) => activeGroups.has(edgeGroupOf(e.type)));
  }
  edges = edges.filter((e) => nodeIds.has(e.from) && nodeIds.has(e.to));

  return { nodes, edges };
}
