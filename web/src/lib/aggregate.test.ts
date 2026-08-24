import { describe, it, expect } from "vitest";
import { aggregateServices, isBridgeOnlyService, serviceNodeId, SERVICE_NODE_TYPE } from "./aggregate";
import { GraphNode, GraphEdge } from "./types";

const node = (id: string, service: string, meta?: Record<string, string>): GraphNode => ({
  id, type: "function", label: id, service, file: "", line: 0, language: "", meta,
});

describe("aggregateServices (high-level view)", () => {
  const nodes = [node("a1", "svc-a"), node("a2", "svc-a"), node("b1", "svc-b")];
  const edges: GraphEdge[] = [
    { id: "e1", from: "a1", to: "b1", type: "http_call" },
    { id: "e2", from: "a2", to: "b1", type: "http_call" },
    { id: "e3", from: "a1", to: "a2", type: "calls" }, // same-service
  ];

  it("produces one node per service with node counts", () => {
    const r = aggregateServices(nodes, edges);
    expect(r.nodes.map((n) => n.id)).toEqual([serviceNodeId("svc-a"), serviceNodeId("svc-b")]);
    expect(r.nodes[0].type).toBe(SERVICE_NODE_TYPE);
    expect(r.nodes[0].meta?.node_count).toBe("2");
  });

  it("aggregates cross-service edges per type with counts, dropping same-service edges", () => {
    const r = aggregateServices(nodes, edges);
    expect(r.edges).toHaveLength(1);
    expect(r.edges[0].from).toBe(serviceNodeId("svc-a"));
    expect(r.edges[0].to).toBe(serviceNodeId("svc-b"));
    expect(r.edges[0].label).toBe("http_call ×2");
    expect(r.edges[0].meta?.bidirectional).toBe("false");
  });

  it("collapses both directions between a pair into one edge, flagged bidirectional", () => {
    const biNodes = [node("w1", "web"), node("p1", "polyflow")];
    const biEdges: GraphEdge[] = [
      { id: "e1", from: "w1", to: "p1", type: "http_call" },
      { id: "e2", from: "p1", to: "w1", type: "sse_endpoint" },
    ];
    const r = aggregateServices(biNodes, biEdges);
    expect(r.edges).toHaveLength(1);
    expect(r.edges[0].meta?.bidirectional).toBe("true");
    expect(r.edges[0].type).toBe("cross_service");
    expect(r.edges[0].label).toBe("2 edges");
  });
});

describe("isBridgeOnlyService (Tier GR)", () => {
  it("is false for a service with at least one real (non-bridge-copied) node", () => {
    const nodes = [node("a1", "willow"), node("a2", "willow", { owner_service: "willow" })];
    expect(isBridgeOnlyService(nodes, "willow")).toBe(false);
  });

  it("is true when every node for the service carries meta.owner_service", () => {
    const nodes = [
      node("d1", "maple-agent", { owner_service: "maple-agent" }),
      node("d2", "maple-agent", { owner_service: "maple-agent" }),
    ];
    expect(isBridgeOnlyService(nodes, "maple-agent")).toBe(true);
  });

  it("is false for a service with no nodes at all (nothing to be bridge-only about)", () => {
    expect(isBridgeOnlyService([node("a1", "willow")], "nope")).toBe(false);
  });
});

describe("aggregateServices bridge_only tagging (Tier GR)", () => {
  it("tags a pill meta.bridge_only when every node for that service is a bridge copy", () => {
    const nodes = [
      node("m1", "willow"),
      node("d1", "maple-agent", { owner_service: "maple-agent" }),
    ];
    const r = aggregateServices(nodes, []);
    const willow = r.nodes.find((n) => n.id === serviceNodeId("willow"));
    const mapleAgent = r.nodes.find((n) => n.id === serviceNodeId("maple-agent"));
    expect(willow?.meta?.bridge_only).toBeUndefined();
    expect(mapleAgent?.meta?.bridge_only).toBe("true");
  });
});
