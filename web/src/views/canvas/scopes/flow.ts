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
  // UF.3: set for a seam whose edge kind /api/seam couldn't expand past its
  // own two endpoints (`expanded: false`) — FlowLane shows this as a small
  // non-blocking note next to the (still-rendered) pair, per the "never an
  // error" rule for an edge kind the seam endpoint can't widen.
  note?: string;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function parseHop(raw: any): FlowHop {
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
export function parseChain(raw: any[] | undefined): FlowChain {
  return { hops: (raw ?? []).map(parseHop) };
}

// Worst-first ranking so "min across a group of chains" always picks the
// least-trustworthy state present, never silently defaulting to the best
// one when a mix is present. A hop with no explicit state is a solid,
// resolved static edge (FlowLane's default un-dashed rendering) — ranked
// best, not unknown.
const VERIFICATION_RANK: Record<string, number> = {
  verified: 3,
  candidate: 2,
  conflicting: 1,
  observed_only_gap: 0,
};

export function minVerificationState(chains: FlowChain[]): string {
  let worst = "verified";
  let worstRank = VERIFICATION_RANK[worst];
  for (const chain of chains) {
    for (const hop of chain.hops) {
      const state = hop.verificationState ?? "verified";
      const rank = VERIFICATION_RANK[state] ?? VERIFICATION_RANK.verified;
      if (rank < worstRank) {
        worst = state;
        worstRank = rank;
      }
    }
  }
  return worst;
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

// UF.3: seam node id prefix — FlowLane's stylesheet selects on this to
// render the channel as a distinct pill rather than a regular flow node.
export const SEAM_CHANNEL_PREFIX = "seam-channel:";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export interface SeamBody {
  channel: string;
  verification_state?: string;
  expanded: boolean;
  sources?: { provider: string; confidence: string; ref?: string }[];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  producers: { node: any; chain: any[] }[];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  consumers: { node: any; chain: any[] }[];
}

export async function fetchSeam(edgeId: string, signal?: AbortSignal): Promise<SeamBody> {
  const r = await apiFetch(`/api/seam/${encodeURIComponent(edgeId)}`, { signal, silent: true });
  return (await r.json()) as SeamBody;
}

// A P×C cross product of every producer chain against every consumer chain,
// spliced through one synthetic channel hop — this is what makes the
// generic hop-rank/service-lane engine (computeFlowLaneLayout) place
// producers left of the channel and consumers right of it "for free",
// without a bespoke seam layout: each combined chain's ranks run ancestor →
// … → producer → channel → consumer → … → terminus. Capped at 200 combined
// chains (e.g. 14 producers × 14 consumers) — a real fan-out this wide would
// blow the canvas budget anyway, so this is a determinism/perf guard, not a
// meaningful UX limit.
const MAX_SEAM_COMBINATIONS = 200;

export function seamChains(body: SeamBody, edgeId: string): FlowChain[] {
  const channelHop: FlowHop = {
    nodeId: `${SEAM_CHANNEL_PREFIX}${edgeId}`,
    label: body.channel || "channel",
    service: "channel",
    verificationState: body.verification_state,
  };
  const producerChains = body.producers.length ? body.producers.map((p) => parseChain(p.chain)) : [{ hops: [] }];
  const consumerChains = body.consumers.length ? body.consumers.map((c) => parseChain(c.chain)) : [{ hops: [] }];

  const chains: FlowChain[] = [];
  outer: for (const p of producerChains) {
    for (const c of consumerChains) {
      if (chains.length >= MAX_SEAM_COMBINATIONS) break outer;
      chains.push({ hops: [...p.hops, channelHop, ...c.hops] });
    }
  }
  return chains;
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
      const body: SeamBody = await fetchSeam(ref.edgeId, signal);
      return {
        chains: seamChains(body, ref.edgeId),
        truncated: false,
        reachable: true,
        label: `Seam: ${body.channel || ref.edgeId}`,
        note: body.expanded ? undefined : "No channel closure — this edge kind has nothing to expand past its own pair.",
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
