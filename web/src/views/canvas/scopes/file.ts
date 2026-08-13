// One file's symbols and the intra-file edges between them, via the
// dedicated GET /api/scope?kind=file endpoint (internal/server/handlers.go's
// handleScope). Edges leaving the file arrive with their external endpoint
// flagged meta.stub="true" by the server; this resolver fills in where a
// click on that stub should take you (always "that node's file scope" —
// file granularity keeps the click target unambiguous).
import { Scope } from "../../../stores/scope";
import { apiFetchJSON } from "../../../lib/apiFetch";
import { GraphData, sortGraphData } from "./common";

// Mirrors internal/server/handlers.go's handleScope response.
interface ApiScopeNodeData {
  id: string;
  label: string;
  type: string;
  service: string;
  file: string;
  line: number;
  end_line?: number;
  language: string;
  meta?: Record<string, string>;
}
interface ApiScopeEdgeData {
  id: string;
  source: string;
  target: string;
  type: string;
  label?: string;
  confidence?: string;
  meta?: Record<string, string>;
}
interface ApiScopeResult {
  kind: string;
  file: string;
  service: string;
  nodes: { data: ApiScopeNodeData }[];
  edges: { data: ApiScopeEdgeData }[];
}

export async function resolveFile(scope: Extract<Scope, { kind: "file" }>, signal?: AbortSignal): Promise<GraphData> {
  const params = new URLSearchParams({ kind: "file", path: scope.path });
  if (scope.service) params.set("service", scope.service);
  const resp = await apiFetchJSON<ApiScopeResult>(`/api/scope?${params}`, { signal });

  const nodes = resp.nodes.map((n) => {
    const isStub = n.data.meta?.stub === "true";
    return {
      id: n.data.id,
      type: n.data.type,
      label: n.data.label,
      service: n.data.service,
      file: n.data.file,
      line: n.data.line,
      language: n.data.language,
      meta: isStub
        ? { ...n.data.meta, stub_kind: "file", stub_service: n.data.service, stub_path: n.data.file }
        : n.data.meta,
    };
  });

  const edges = resp.edges.map((e) => ({
    id: e.data.id,
    from: e.data.source,
    to: e.data.target,
    type: e.data.type,
    label: e.data.label,
    confidence: e.data.confidence,
    meta: e.data.meta,
  }));

  return sortGraphData({ nodes, edges });
}
