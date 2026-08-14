import { describe, it, expect, vi } from "vitest";
import { resolveFlow, computeFlowLaneLayout, flowRefLabel, FlowChain } from "./flow";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const THROUGH_BODY = {
  flows: [
    {
      entrypoint: { node_id: "root", kind: "route", label: "POST /orders", service: "rails-svc" },
      chain: [
        { node_id: "root", label: "POST /orders", service: "rails-svc" },
        { node_id: "mid", label: "OrdersController#create", service: "rails-svc", edge_type: "calls" },
        { node_id: "leaf", label: "publish", service: "rails-svc", edge_type: "publishes", cross_service: true },
      ],
    },
  ],
  truncated: false,
};

describe("resolveFlow / through", () => {
  it("picks the matching entrypoint's chain and derives the chip label", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/leaf?limit=20": THROUGH_BODY });
    const res = await resolveFlow({ kind: "through", nodeId: "leaf", entrypointId: "root" });
    expect(res.reachable).toBe(true);
    expect(res.truncated).toBe(false);
    expect(res.chains).toHaveLength(1);
    expect(res.chains[0].hops.map((h) => h.nodeId)).toEqual(["root", "mid", "leaf"]);
    expect(res.label).toBe("POST /orders → publish");
  });

  it("propagates truncated:true and re-queries with a larger limit", async () => {
    const fetchMock = fakeFetch({
      "/api/flows/through/leaf?limit=20": { ...THROUGH_BODY, truncated: true },
      "/api/flows/through/leaf?limit=40": { ...THROUGH_BODY, truncated: false },
    });
    (globalThis as any).fetch = fetchMock;
    const first = await resolveFlow({ kind: "through", nodeId: "leaf", entrypointId: "root" });
    expect(first.truncated).toBe(true);
    const second = await resolveFlow({ kind: "through", nodeId: "leaf", entrypointId: "root" }, undefined, { throughLimit: 40 });
    expect(second.truncated).toBe(false);
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/leaf?limit=20": THROUGH_BODY });
    const a = await resolveFlow({ kind: "through", nodeId: "leaf", entrypointId: "root" });
    const b = await resolveFlow({ kind: "through", nodeId: "leaf", entrypointId: "root" });
    expect(a).toEqual(b);
  });
});

describe("resolveFlow / path", () => {
  it("returns the honest empty result when unreachable", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/paths?from=a&to=b&k=20": { paths: [], reachable: false } });
    const res = await resolveFlow({ kind: "path", from: "a", to: "b", index: 0 });
    expect(res.reachable).toBe(false);
    expect(res.chains).toEqual([]);
    expect(res.label).toBe("No static path");
  });

  it("picks the path at `index`", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=a&to=b&k=20": {
        reachable: true,
        paths: [
          { chain: [{ node_id: "a", label: "A", service: "svc" }, { node_id: "b", label: "B", service: "svc" }] },
          { chain: [{ node_id: "a", label: "A", service: "svc" }, { node_id: "c", label: "C", service: "svc" }, { node_id: "b", label: "B", service: "svc" }] },
        ],
      },
    });
    const res = await resolveFlow({ kind: "path", from: "a", to: "b", index: 1 });
    expect(res.chains[0].hops.map((h) => h.nodeId)).toEqual(["a", "c", "b"]);
  });
});

describe("resolveFlow / seam", () => {
  it("splices a synthetic channel hop into every producer×consumer combination (rule 1 fan-out)", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/seam/edge-1": {
        channel: "rabbitmq:cdr_requests",
        verification_state: "verified",
        expanded: true,
        producers: [{ node: { id: "p1" }, chain: [{ node_id: "p1", label: "Publisher", service: "rails-svc" }] }],
        consumers: [
          { node: { id: "c1" }, chain: [{ node_id: "c1", label: "Consumer1", service: "rails-consumer1" }] },
          { node: { id: "c2" }, chain: [{ node_id: "c2", label: "Consumer2", service: "rails-consumer2" }] },
        ],
      },
    });
    const res = await resolveFlow({ kind: "seam", edgeId: "edge-1" });
    // 1 producer × 2 consumers = 2 combined chains, each producer -> channel -> consumer.
    expect(res.chains).toHaveLength(2);
    expect(res.chains[0].hops.map((h) => h.nodeId)).toEqual(["p1", "seam-channel:edge-1", "c1"]);
    expect(res.chains[1].hops.map((h) => h.nodeId)).toEqual(["p1", "seam-channel:edge-1", "c2"]);
    expect(res.reachable).toBe(true);
    expect(res.label).toBe("Seam: rabbitmq:cdr_requests");
    expect(res.note).toBeUndefined();
  });

  it("surfaces an honest note when the edge kind couldn't expand past its own pair", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/seam/edge-2": {
        channel: "calls",
        expanded: false,
        producers: [{ node: { id: "a" }, chain: [{ node_id: "a", label: "A", service: "svc" }] }],
        consumers: [{ node: { id: "b" }, chain: [{ node_id: "b", label: "B", service: "svc" }] }],
      },
    });
    const res = await resolveFlow({ kind: "seam", edgeId: "edge-2" });
    expect(res.chains).toHaveLength(1);
    expect(res.note).toMatch(/no channel closure/i);
  });
});

describe("resolveFlow / not-yet-backed kinds", () => {
  it("returns an honest empty result for varflow/edgeset/pins without fetching", async () => {
    const fetchMock = vi.fn();
    (globalThis as any).fetch = fetchMock;
    const varflow = await resolveFlow({ kind: "varflow", nodeId: "n1" });
    const edgeset = await resolveFlow({ kind: "edgeset", nodeId: "n1", edgeTypes: ["calls"] });
    const pins = await resolveFlow({ kind: "pins", ids: ["n1", "n2"] });
    expect(varflow).toEqual({ chains: [], truncated: false, reachable: false, label: "varflow" });
    expect(edgeset.reachable).toBe(false);
    expect(pins.reachable).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("flowRefLabel", () => {
  it("gives a short synchronous label per kind", () => {
    expect(flowRefLabel({ kind: "path", from: "a", to: "b", index: 0 })).toBe("a → b");
    expect(flowRefLabel({ kind: "waypoints", ids: ["a", "b", "c"], direction: "forward" })).toBe("3 waypoints");
    expect(flowRefLabel({ kind: "pins", ids: ["a", "b"] })).toBe("2 pins");
  });
});

describe("computeFlowLaneLayout", () => {
  const chains: FlowChain[] = [
    {
      hops: [
        { nodeId: "n1", label: "Route", service: "rails-svc" },
        { nodeId: "n2", label: "Controller", service: "rails-svc", edgeType: "calls" },
        { nodeId: "n3", label: "Publisher", service: "rails-svc", edgeType: "calls" },
        { nodeId: "n4", label: "Consumer", service: "worker-svc", edgeType: "publishes", crossService: true, edgeLabel: "cdr_requests" },
      ],
    },
    {
      hops: [
        { nodeId: "n1", label: "Route", service: "rails-svc" },
        { nodeId: "n5", label: "Other", service: "another-svc", edgeType: "calls" },
      ],
    },
  ];

  it("assigns one lane per distinct service, lexically ordered", () => {
    const layout = computeFlowLaneLayout(chains);
    expect(layout.services).toEqual(["another-svc", "rails-svc", "worker-svc"]);
  });

  it("preserves hop order as rank, taking the earliest appearance for shared nodes", () => {
    const layout = computeFlowLaneLayout(chains);
    const byId = new Map(layout.nodes.map((n) => [n.id, n]));
    expect(byId.get("n1")!.rank).toBe(0);
    expect(byId.get("n2")!.rank).toBe(1);
    expect(byId.get("n3")!.rank).toBe(2);
    expect(byId.get("n4")!.rank).toBe(3);
    expect(byId.get("n5")!.rank).toBe(1);
  });

  it("dedupes shared hop-to-hop edges across chains and sorts by id", () => {
    const layout = computeFlowLaneLayout(chains);
    expect(layout.edges.map((e) => e.id)).toEqual([...layout.edges.map((e) => e.id)].sort());
    const ids = layout.edges.map((e) => `${e.from}->${e.to}`);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("carries cross-service metadata onto the crossing edge", () => {
    const layout = computeFlowLaneLayout(chains);
    const crossing = layout.edges.find((e) => e.from === "n3" && e.to === "n4")!;
    expect(crossing.crossService).toBe(true);
    expect(crossing.edgeLabel).toBe("cdr_requests");
  });

  it("is deterministic across two runs", () => {
    const a = computeFlowLaneLayout(chains);
    const b = computeFlowLaneLayout(chains);
    expect(a).toEqual(b);
  });
});
