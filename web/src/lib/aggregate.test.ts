import { describe, it, expect } from "vitest";
import { aggregateServices, serviceNodeId, SERVICE_NODE_TYPE } from "./aggregate";
import { GraphNode, GraphEdge } from "./types";

const node = (id: string, service: string): GraphNode => ({
  id, type: "function", label: id, service, file: "", line: 0, language: "",
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
  });
});
