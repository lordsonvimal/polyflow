import { describe, it, expect } from "vitest";
import { resolvePinboard, filterChainsByLens, pinboardMemberIds } from "./pinboard";
import { FlowChain } from "./flow";

function fakeFetch(routes: Record<string, unknown>) {
  return (url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: true, json: async () => ({ paths: [], reachable: false }) } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  };
}

function chain(...ids: string[]): { chain: unknown[] } {
  return { chain: ids.map((id) => ({ node_id: id, label: id, service: "svc" })) };
}

describe("resolvePinboard / 2 pins", () => {
  it("keeps every branch a direct path fetch returns — nothing to intersect with only 2 pins", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=p0&to=p1&k=5": {
        reachable: true,
        paths: [chain("p0", "x", "p1"), chain("p0", "y", "p1")],
      },
    });
    const res = await resolvePinboard(["p0", "p1"]);
    expect(res.reachable).toBe(true);
    expect(res.chains).toHaveLength(2);
    expect(res.chains.map((c) => c.hops.map((h) => h.nodeId))).toEqual([
      ["p0", "x", "p1"],
      ["p0", "y", "p1"],
    ]);
  });

  it("tries the reverse direction when the forward pair has no path", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=p0&to=p1&k=5": { reachable: false, paths: [] },
      "/api/flows/paths?from=p1&to=p0&k=5": { reachable: true, paths: [chain("p1", "p0")] },
    });
    const res = await resolvePinboard(["p0", "p1"]);
    expect(res.reachable).toBe(true);
    expect(res.chains[0].hops.map((h) => h.nodeId)).toEqual(["p1", "p0"]);
  });

  it("names the broken pair when neither direction connects", async () => {
    (globalThis as any).fetch = fakeFetch({});
    const res = await resolvePinboard(["p0", "p1"]);
    expect(res.reachable).toBe(false);
    expect(res.chains).toEqual([]);
    expect(res.brokenPair).toEqual({ from: "p0", to: "p1" });
  });
});

describe("resolvePinboard / 3 pins, diamond", () => {
  // p0 -[x]-> p1 and p0 -[y]-> p1 are two parallel branches, but the third
  // pin M only sits on the x branch (p0->M, M->p1) — pinning M must make
  // the y branch disappear from the result even though p0->p1 alone would
  // include it, since a pinboard chain is only ever queried through its
  // pin-adjacent segments, never the direct endpoint pair.
  it("drops the non-through branch once the middle pin forces a specific route", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=p0&to=m&k=5": { reachable: true, paths: [chain("p0", "x", "m")] },
      "/api/flows/paths?from=m&to=p1&k=5": { reachable: true, paths: [chain("m", "p1")] },
    });
    const res = await resolvePinboard(["p0", "m", "p1"]);
    expect(res.reachable).toBe(true);
    expect(res.chains).toHaveLength(1);
    expect(res.chains[0].hops.map((h) => h.nodeId)).toEqual(["p0", "x", "m", "p1"]);
  });

  it("is order-free: finds a connecting ordering regardless of pin insertion order", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=p0&to=m&k=5": { reachable: true, paths: [chain("p0", "m")] },
      "/api/flows/paths?from=m&to=p1&k=5": { reachable: true, paths: [chain("m", "p1")] },
    });
    // Pinned in the order p1, p0, m — none of which is directly connectable
    // adjacent-in-insertion-order (p1->p0 and p0->m aren't both fetched
    // routes), but permutation search still finds p0->m->p1.
    const res = await resolvePinboard(["p1", "p0", "m"]);
    expect(res.reachable).toBe(true);
    expect(res.chains[0].hops.map((h) => h.nodeId)).toEqual(["p0", "m", "p1"]);
  });

  it("names the first broken adjacent pair when no ordering connects all three", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=p0&to=m&k=5": { reachable: true, paths: [chain("p0", "m")] },
      // m<->p1 has no path in either direction, so nothing can stitch p1 in.
    });
    const res = await resolvePinboard(["p0", "m", "p1"]);
    expect(res.reachable).toBe(false);
    expect(res.brokenPair).toEqual({ from: "m", to: "p1" });
  });
});

describe("filterChainsByLens", () => {
  const chains: FlowChain[] = [
    { hops: [{ nodeId: "a", label: "A", service: "svc" }, { nodeId: "b", label: "B", service: "svc", edgeType: "calls" }] },
    { hops: [{ nodeId: "a", label: "A", service: "svc" }, { nodeId: "c", label: "C", service: "svc", edgeType: "publishes" }] },
  ];

  it("keeps everything under the All lens", () => {
    expect(filterChainsByLens(chains, "All")).toEqual(chains);
  });

  it("narrows to chains whose hops all fall inside the lens's edge types", () => {
    const result = filterChainsByLens(chains, "Calls");
    expect(result).toHaveLength(1);
    expect(result[0].hops.map((h) => h.nodeId)).toEqual(["a", "b"]);
  });
});

describe("pinboardMemberIds", () => {
  it("unions every hop node id across all chains", () => {
    const chains: FlowChain[] = [
      { hops: [{ nodeId: "a", label: "A", service: "svc" }, { nodeId: "b", label: "B", service: "svc" }] },
      { hops: [{ nodeId: "a", label: "A", service: "svc" }, { nodeId: "c", label: "C", service: "svc" }] },
    ];
    expect(pinboardMemberIds(chains)).toEqual(new Set(["a", "b", "c"]));
  });
});
