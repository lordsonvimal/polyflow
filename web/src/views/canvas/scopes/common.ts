// Shared helpers for the UN.1 scope resolvers (overview/service/folder/
// file/neighborhood). Each resolver returns this shape; CanvasHost turns it
// into Cytoscape elements.
import { GraphNode, GraphEdge } from "../../../lib/types";
import { apiFetch } from "../../../lib/apiFetch";

export interface GraphData {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export function parseCytoGraph(raw: unknown): GraphData {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const r = raw as { nodes?: any[]; edges?: any[] };
  return {
    nodes: (r.nodes ?? []).map((n) => ({
      id: n.data.id,
      type: n.data.type,
      label: n.data.label,
      service: n.data.service ?? "",
      file: n.data.file ?? "",
      line: n.data.line ?? 0,
      language: n.data.language ?? "",
      meta: n.data.meta,
    })),
    edges: (r.edges ?? []).map((e) => ({
      id: e.data.id,
      from: e.data.source,
      to: e.data.target,
      type: e.data.type,
      label: e.data.label,
      confidence: e.data.confidence,
      verificationState: e.data.verification_state,
      meta: e.data.meta,
    })),
  };
}

export async function fetchAllGraph(signal?: AbortSignal): Promise<GraphData> {
  const limit = 2000;
  const nodes: GraphNode[] = [];
  const edges: GraphEdge[] = [];
  for (let page = 1; ; page++) {
    const r = await apiFetch(`/api/graph?limit=${limit}&page=${page}`, {
      signal,
      silent: true,
    });
    const d = parseCytoGraph(await r.json());
    nodes.push(...d.nodes);
    edges.push(...d.edges);
    if (d.nodes.length < limit) break;
  }
  return { nodes, edges };
}

// Rule 2 (docs/phases.md, bug-class rules): deterministic output, always —
// every resolver sorts by id before its elements reach Cytoscape, so a Map
// iteration upstream can never leak into render order.
export function sortGraphData(d: GraphData): GraphData {
  return {
    nodes: [...d.nodes].sort((a, b) => a.id.localeCompare(b.id)),
    edges: [...d.edges].sort((a, b) => a.id.localeCompare(b.id)),
  };
}

export type StubKind = "service" | "folder" | "file";

// A stub connector: a real node id (or a synthetic group id) standing in
// for something outside the current scope. Clicking it pushes the scope
// that would bring it into view (CanvasHost reads meta.stub_*) — plan-10's
// "never silently no-ops" applied to boundary edges.
export function stubNode(
  id: string,
  label: string,
  service: string,
  kind: StubKind,
  path: string,
  nodeCount?: number,
): GraphNode {
  return {
    id,
    type: kind,
    label,
    service,
    file: kind === "file" ? path : "",
    line: 0,
    language: "",
    meta: {
      stub: "true",
      stub_kind: kind,
      stub_service: service,
      stub_path: path,
      ...(nodeCount !== undefined ? { node_count: String(nodeCount) } : {}),
    },
  };
}
