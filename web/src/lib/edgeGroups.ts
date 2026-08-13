// FilterBar's coarse edge-type groups (UN.2). Every graph.EdgeType (see
// internal/graph/model.go) is assigned to exactly one group so the filter
// surface never leaves a type unfilterable. This is a provisional partition
// for the filter chips only — UN.5's lens table is the pinned, rule-12
// enum-coverage-tested mapping and may group/name these differently (e.g.
// it carves "imports" out of "structure"); that table supersedes this one
// for lens semantics, not for FilterBar's six named chips.
export const EDGE_GROUPS: Record<string, string[]> = {
  calls: ["calls", "spawns", "instantiates"],
  http: [
    "http_call", "sse_endpoint", "grpc_call", "graphql_call", "navigates_to",
    "ws_upgrade", "ws_connect", "ws_read", "ws_write", "ws_send", "cloud_call",
  ],
  messaging: [
    "publishes", "subscribes", "kafka_publish", "nats_publish", "redis_publish",
    "job_enqueue", "job_perform", "sidekiq_enqueue", "sidekiq_perform",
    "pusher_trigger", "pusher_subscribe", "hub_subscribe", "hub_broadcast",
  ],
  "data-flow": [
    "declares", "reads", "writes", "captures", "flows_to", "queries",
    "persists", "returns", "consumes", "response_of",
  ],
  dom: [
    "dom_read", "dom_write", "dom_create", "dom_remove", "dom_listen",
    "dom_contract", "datastar_action", "datastar_bind", "renders", "defined_in",
  ],
  structure: [
    "contains", "imports", "uses_type", "inherits", "implements", "component_impl",
  ],
};

export const EDGE_GROUP_NAMES = Object.keys(EDGE_GROUPS);

// Group an edge type belongs to, or "structure" as the residual bucket for
// any type not yet listed above (keeps an unrecognized/new type filterable
// rather than invisible to every chip).
export function edgeGroupOf(type: string): string {
  for (const [group, types] of Object.entries(EDGE_GROUPS)) {
    if (types.includes(type)) return group;
  }
  return "structure";
}

// Flattens a set of active group names to the concrete edge types they cover.
export function edgeTypesForGroups(groups: readonly string[]): string[] {
  return groups.flatMap((g) => EDGE_GROUPS[g] ?? []);
}
