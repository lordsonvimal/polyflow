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
  // Tier NV.7. OPPOSITE polarity from edgeTypes/services: [] = "show
  // nothing extra" (hide every noise-classified edge), matching the
  // agent-side trace/context/impact default. Do not "fix" this to match
  // the other axes' []-means-everything convention — see scope.ts's
  // ViewState["filters"] comment. Optional so existing GraphFilters call
  // sites (and older decoded ViewState hashes) that predate this field
  // still compile/behave as "hide noise" by default.
  noiseClasses?: string[];
}

// [] on edgeTypes/services means "no explicit restriction" (show
// everything). [] on confidence falls back to lib/confidence.ts's
// DEFAULT_CONFIDENCE (static+inferred) — partial/unknown stay opt-in even
// before any chip has ever been touched. [] on noiseClasses means the
// opposite: hide every noise-classified edge (see GraphFilters above).
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

  const activeNoiseClasses = new Set(filters.noiseClasses ?? []);
  edges = edges.filter((e) => {
    const noiseClass = e.meta?.noise_class;
    return !noiseClass || activeNoiseClasses.has(noiseClass);
  });

  edges = edges.filter((e) => nodeIds.has(e.from) && nodeIds.has(e.to));

  return { nodes, edges };
}

// Tier NV.7: the "Noise (N hidden)" badge FilterBar shows on its Noise row
// — the client-side equivalent of trace/context/impact's `hidden_by_class`,
// satisfying the same "never silently dropped" rule via a visible count
// instead of a JSON field. Counted against the lensed-but-pre-chip edge set
// so the number reflects "how much noise exists in this scope" independent
// of confidence/edgeType/service chip state.
export function countHiddenByNoise(edges: GraphEdge[], activeNoiseClasses: readonly string[]): number {
  const active = new Set(activeNoiseClasses);
  return edges.filter((e) => e.meta?.noise_class && !active.has(e.meta.noise_class)).length;
}
