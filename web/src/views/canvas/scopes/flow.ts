// UF.0: the flow-lane scope resolver. Unlike the drill resolvers
// (overview/service/folder/file/neighborhood), a flow's input is a *chain
// list* (UB.5 shape: ordered hops with edge metadata), not a node/edge
// graph — FlowLane.tsx lays it out as swimlanes rather than handing it to
// the shared budget/lens/filter pipeline.
import { FlowRef } from "../../../stores/scope";
import { apiFetch } from "../../../lib/apiFetch";

export interface FlowHop {
  nodeId: string;
  label: string;
  service: string;
  edgeType?: string;
  edgeLabel?: string;
  crossService?: boolean;
  confidence?: string;
  verificationState?: string;
}

export interface FlowChain {
  hops: FlowHop[];
}

export interface FlowResolution {
  chains: FlowChain[];
  // UB.5 `truncated: true` on /api/flows/through — more chains exist past
  // the fetched limit; FlowLane renders an end-cap that re-queries deeper.
  truncated: boolean;
  // False only for an honest "no path" result (UB.5 `reachable: false`),
  // never for a request that simply hasn't been wired up yet.
  reachable: boolean;
  // Breadcrumb chip text: "<entrypoint label> → <terminus label>".
  label: string;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function parseHop(raw: any): FlowHop {
  return {
    nodeId: raw.node_id,
    label: raw.label,
    service: raw.service ?? "",
    edgeType: raw.edge_type || undefined,
    edgeLabel: raw.edge_label || undefined,
    crossService: !!raw.cross_service,
    confidence: raw.confidence || undefined,
    verificationState: raw.verification_state || undefined,
  };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function parseChain(raw: any[] | undefined): FlowChain {
  return { hops: (raw ?? []).map(parseHop) };
}

function chainLabel(chain: FlowChain, fallback: string): string {
  const first = chain.hops[0];
  const last = chain.hops.at(-1);
  if (!first || !last) return fallback;
  return first.nodeId === last.nodeId ? first.label : `${first.label} → ${last.label}`;
}

// A short, synchronous label for the FlowRef itself — used by the generic
// scope-stack Breadcrumbs, which has no chain data to derive a full
// "entrypoint → terminus" chip from without an extra fetch.
export function flowRefLabel(ref: FlowRef): string {
  switch (ref.kind) {
    case "through": return "flow";
    case "path": return `${ref.from} → ${ref.to}`;
    case "waypoints": return `${ref.ids.length} waypoints`;
    case "seam": return "seam";
    case "varflow": return "varflow";
    case "edgeset": return `${ref.nodeId} (${ref.edgeTypes.join(", ")})`;
    case "pins": return `${ref.ids.length} pins`;
  }
}

// UF.0 wires the four FlowRef kinds already backed by a UB.5 endpoint
// (through/path/waypoints/seam). varflow/edgeset/pins compose flows built by
// later phases (UF.4, the UN.5 lens tie-in, UF.7's pinboard intersection) —
// an honest empty result here rather than guessing at a request shape.
export async function resolveFlow(
  ref: FlowRef,
  signal?: AbortSignal,
  opts?: { throughLimit?: number },
): Promise<FlowResolution> {
  switch (ref.kind) {
    case "through": {
      const limit = opts?.throughLimit ?? 20;
      const p = new URLSearchParams({ limit: String(limit) });
      const r = await apiFetch(`/api/flows/through/${encodeURIComponent(ref.nodeId)}?${p}`, { signal, silent: true });
      const body = (await r.json()) as {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        flows: { entrypoint: any; chain: any[] }[];
        truncated: boolean;
      };
      const match = body.flows.find((f) => f.entrypoint?.node_id === ref.entrypointId);
      const picked = match ? [match] : body.flows;
      const chains = picked.map((f) => parseChain(f.chain));
      const entrypointLabel = picked[0]?.entrypoint?.label as string | undefined;
      return {
        chains,
        truncated: body.truncated,
        reachable: chains.length > 0,
        label: chains[0] ? chainLabel(chains[0], entrypointLabel ?? "flow") : "flow",
      };
    }
    case "path": {
      const p = new URLSearchParams({ from: ref.from, to: ref.to, k: "20" });
      const r = await apiFetch(`/api/flows/paths?${p}`, { signal, silent: true });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const body = (await r.json()) as { paths: { chain: any[] }[]; reachable: boolean };
      const picked = body.paths[ref.index];
      const chain = picked ? parseChain(picked.chain) : null;
      return {
        chains: chain ? [chain] : [],
        truncated: false,
        reachable: body.reachable && !!chain,
        label: chain ? chainLabel(chain, "flow") : "No static path",
      };
    }
    case "waypoints": {
      const p = new URLSearchParams({ waypoints: ref.ids.join(","), direction: ref.direction });
      const r = await apiFetch(`/api/flows/refine?${p}`, { signal, silent: true });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const body = (await r.json()) as { chain: any[] };
      const chain = parseChain(body.chain);
      return { chains: [chain], truncated: false, reachable: chain.hops.length > 0, label: chainLabel(chain, "flow") };
    }
    case "seam": {
      const r = await apiFetch(`/api/seam/${encodeURIComponent(ref.edgeId)}`, { signal, silent: true });
      const body = (await r.json()) as {
        channel: string;
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        producers: { node: any; chain: any[] }[];
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        consumers: { node: any; chain: any[] }[];
      };
      const chains = [...body.producers, ...body.consumers].map((s) => parseChain(s.chain));
      return {
        chains,
        truncated: false,
        reachable: chains.length > 0,
        label: `Seam: ${body.channel || ref.edgeId}`,
      };
    }
    case "varflow":
    case "edgeset":
    case "pins":
      return { chains: [], truncated: false, reachable: false, label: flowRefLabel(ref) };
  }
}

export interface LaneNode {
  id: string;
  label: string;
  service: string;
  rank: number;
  lane: number;
  verificationState?: string;
}

export interface LaneEdge {
  id: string;
  from: string;
  to: string;
  edgeType?: string;
  edgeLabel?: string;
  crossService?: boolean;
  confidence?: string;
  verificationState?: string;
}

export interface FlowLaneLayout {
  // Lane order, top to bottom — services sorted lexically for determinism
  // (rule 2), not first-appearance order.
  services: string[];
  nodes: LaneNode[];
  edges: LaneEdge[];
}

// Pure layout math: left→right by hop order, one row per service. A node
// appearing in multiple chains at different hop indices takes the smallest
// (renders as early as its earliest appearance); duplicate hop-to-hop edges
// across chains collapse to one. Both node and edge lists sort by id, so two
// calls on the same input always produce byte-identical output.
export function computeFlowLaneLayout(chains: FlowChain[]): FlowLaneLayout {
  const rankById = new Map<string, number>();
  const hopById = new Map<string, FlowHop>();
  for (const chain of chains) {
    chain.hops.forEach((hop, i) => {
      const prevRank = rankById.get(hop.nodeId);
      if (prevRank === undefined || i < prevRank) rankById.set(hop.nodeId, i);
      if (!hopById.has(hop.nodeId)) hopById.set(hop.nodeId, hop);
    });
  }

  const services = [...new Set([...hopById.values()].map((h) => h.service))].sort();
  const laneIndex = new Map(services.map((s, i) => [s, i]));

  const nodes: LaneNode[] = [...hopById.values()]
    .map((h) => ({
      id: h.nodeId,
      label: h.label,
      service: h.service,
      rank: rankById.get(h.nodeId) ?? 0,
      lane: laneIndex.get(h.service) ?? 0,
      verificationState: h.verificationState,
    }))
    .sort((a, b) => a.id.localeCompare(b.id));

  const edgeMap = new Map<string, LaneEdge>();
  for (const chain of chains) {
    for (let i = 1; i < chain.hops.length; i++) {
      const from = chain.hops[i - 1];
      const to = chain.hops[i];
      const key = `${from.nodeId}->${to.nodeId}:${to.edgeType ?? ""}`;
      if (!edgeMap.has(key)) {
        edgeMap.set(key, {
          id: key,
          from: from.nodeId,
          to: to.nodeId,
          edgeType: to.edgeType,
          edgeLabel: to.edgeLabel,
          crossService: to.crossService,
          confidence: to.confidence,
          verificationState: to.verificationState,
        });
      }
    }
  }
  const edges = [...edgeMap.values()].sort((a, b) => a.id.localeCompare(b.id));

  return { services, nodes, edges };
}
