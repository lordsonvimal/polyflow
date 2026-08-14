// UF.7: pinboard resolution — "only nodes/edges lying on some flow path
// passing through ALL pins" (order-free). No dedicated backend endpoint
// exists (nor is one needed): this composes the two UB.5 endpoints already
// wired for UF.0/UF.2 (`/api/flows/paths`, k-shortest between two nodes) the
// same way a human would — query every consecutive pair for a pin ordering
// that fully connects, then splice those segments into one chain.
//
// "Consecutive" pairs come from a *found* ordering, not insertion order:
// pins are unordered, so this tries every permutation (capped — see
// MAX_PINBOARD_PERMUTE) and keeps the first that connects end-to-end. A pin
// standing on only one branch of a diamond (two parallel paths between its
// neighbors) naturally drops the other branch — that branch is never
// fetched, since only the pin-adjacent segments are queried, not the direct
// endpoint-to-endpoint pair.
import { apiFetch, ApiError } from "../../../lib/apiFetch";
import { FlowChain, parseChain } from "./flow";
import { edgeTypesForLens } from "../lenses";

export interface PinboardResolution {
  chains: FlowChain[];
  reachable: boolean;
  // Honest empty result: the first adjacent (pin-insertion-order) pair with
  // no path in either direction — the UI names this pair and offers
  // "remove <node>" (per-pair reachability, never a silent blank canvas).
  brokenPair?: { from: string; to: string };
}

// n! guard on the ordering search — real pinboards are a handful of nodes
// (the acceptance example pins 2-3); beyond this, only the as-pinned order
// is tried (a documented degrade, not a crash or a hang).
const MAX_PINBOARD_PERMUTE = 6;
// Same fan-out discipline as seam's MAX_SEAM_COMBINATIONS: caps the
// cartesian stitch across segments so a wide multi-pin fan-out can't blow
// past the canvas budget anyway.
const MAX_PINBOARD_COMBINATIONS = 200;

function permutations<T>(arr: T[]): T[][] {
  if (arr.length <= 1) return [arr.slice()];
  const out: T[][] = [];
  for (let i = 0; i < arr.length; i++) {
    const rest = [...arr.slice(0, i), ...arr.slice(i + 1)];
    for (const p of permutations(rest)) out.push([arr[i], ...p]);
  }
  return out;
}

// Splices two chains that share a boundary pin (the first chain's last hop
// equals the second chain's first hop) — drops the duplicate hop instance.
function splice2(a: FlowChain, b: FlowChain): FlowChain {
  return { hops: [...a.hops, ...b.hops.slice(1)] };
}

export async function resolvePinboard(pinIds: string[], signal?: AbortSignal): Promise<PinboardResolution> {
  if (pinIds.length < 2) return { chains: [], reachable: false };

  const cache = new Map<string, FlowChain[]>();
  async function fetchPair(from: string, to: string): Promise<FlowChain[]> {
    const key = `${from}->${to}`;
    const cached = cache.get(key);
    if (cached) return cached;
    const p = new URLSearchParams({ from, to, k: "5" });
    let chains: FlowChain[];
    try {
      const r = await apiFetch(`/api/flows/paths?${p}`, { signal, silent: true });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const body = (await r.json()) as { paths: { chain: any[] }[]; reachable: boolean };
      chains = body.paths.map((x) => parseChain(x.chain));
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") throw err;
      // A stale/unknown pin id 404s — treat as "no path", not a hard failure.
      if (err instanceof ApiError) chains = [];
      else throw err;
    }
    cache.set(key, chains);
    return chains;
  }

  const orderings = pinIds.length <= MAX_PINBOARD_PERMUTE ? permutations(pinIds) : [pinIds.slice()];

  for (const order of orderings) {
    const segmentSets: FlowChain[][] = [];
    let connected = true;
    for (let i = 0; i < order.length - 1; i++) {
      const segs = await fetchPair(order[i], order[i + 1]);
      if (segs.length === 0) {
        connected = false;
        break;
      }
      segmentSets.push(segs);
    }
    if (!connected) continue;

    let combos: FlowChain[] = segmentSets[0];
    for (let i = 1; i < segmentSets.length; i++) {
      const next: FlowChain[] = [];
      outer: for (const c of combos) {
        for (const s of segmentSets[i]) {
          if (next.length >= MAX_PINBOARD_COMBINATIONS) break outer;
          next.push(splice2(c, s));
        }
      }
      combos = next;
    }
    return { chains: combos, reachable: true };
  }

  // No ordering fully connects — name the first adjacent (as-pinned) pair
  // with no path in either direction as the honest broken link.
  for (let i = 0; i < pinIds.length - 1; i++) {
    const [fwd, bwd] = await Promise.all([
      fetchPair(pinIds[i], pinIds[i + 1]),
      fetchPair(pinIds[i + 1], pinIds[i]),
    ]);
    if (fwd.length === 0 && bwd.length === 0) {
      return { chains: [], reachable: false, brokenPair: { from: pinIds[i], to: pinIds[i + 1] } };
    }
  }
  return { chains: [], reachable: false };
}

// UF.7: "pins compose with the active lens" — no server-side edge-type
// filter exists on /api/flows/paths, so this narrows the resolved chains to
// the ones fully inside the lens's edge set client-side, same axis as
// lenses.ts's applyLens.
export function filterChainsByLens(chains: FlowChain[], lens: string): FlowChain[] {
  const allow = edgeTypesForLens(lens);
  if (!allow) return chains; // "All" lens keeps everything
  return chains.filter((c) => c.hops.every((h) => !h.edgeType || allow.includes(h.edgeType)));
}

export function pinboardMemberIds(chains: FlowChain[]): Set<string> {
  const ids = new Set<string>();
  for (const c of chains) for (const h of c.hops) ids.add(h.nodeId);
  return ids;
}
