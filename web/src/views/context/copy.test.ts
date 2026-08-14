import { describe, it, expect, beforeEach } from "vitest";
import { buildRequest, flowRefToSource, selectionCopySource } from "./copy";
import { canvasElementsStore } from "../../stores/canvasElements";
import type { FlowChain } from "../canvas/scopes/flow";

const OPTS = { mode: "viewed" as const, depth: 2, snippets: true, maxTokens: 8000 };

describe("buildRequest", () => {
  beforeEach(() => {
    canvasElementsStore.setIds(new Set());
    canvasElementsStore.setClusters(new Map());
  });

  it("builds a single-node element for a node source", () => {
    const { request } = buildRequest({ kind: "node", id: "n1" }, OPTS);
    expect(request.elements).toEqual([{ kind: "node", ids: ["n1"] }]);
    expect(request.mode).toBe("viewed");
    expect(request.depth).toBe(2);
    expect(request.snippets).toBe(true);
    expect(request.max_tokens).toBe(8000);
  });

  it("builds a single-edge element for an edge source", () => {
    const { request } = buildRequest({ kind: "edge", id: "e1" }, OPTS);
    expect(request.elements).toEqual([{ kind: "edge", ids: ["e1"] }]);
  });

  it("builds a flow element for a flow source", () => {
    const { request } = buildRequest({ kind: "flow", id: "n1" }, OPTS);
    expect(request.elements).toEqual([{ kind: "flow", ids: ["n1"] }]);
  });

  it("builds a group element with all given ids for a group source", () => {
    const { request } = buildRequest({ kind: "group", ids: ["a", "b"] }, OPTS);
    expect(request.elements).toEqual([{ kind: "group", ids: ["a", "b"] }]);
  });

  it("scope source reads canvasElementsStore and produces a plain node-count note with no clusters", () => {
    canvasElementsStore.setIds(new Set(["a", "b", "c"]));
    const { request, note } = buildRequest({ kind: "scope" }, OPTS);
    expect(request.elements[0].kind).toBe("node");
    expect(request.elements[0].ids.sort()).toEqual(["a", "b", "c"]);
    expect(note).toBe("3 nodes");
  });

  it("scope source expands a collapsed cluster and states the expansion in the note", () => {
    canvasElementsStore.setIds(new Set(["filegrp:svc:a.go", "b"]));
    canvasElementsStore.setClusters(new Map([["filegrp:svc:a.go", ["n1", "n2"]]]));
    const { request, note } = buildRequest({ kind: "scope" }, OPTS);
    expect(request.elements[0].ids.sort()).toEqual(["b", "n1", "n2"]);
    expect(note).toBe("3 nodes (1 cluster expanded)");
  });
});

describe("flowRefToSource", () => {
  it("maps a seam ref to an edge source", () => {
    expect(flowRefToSource({ kind: "seam", edgeId: "e1" }, [])).toEqual({ kind: "edge", id: "e1" });
  });

  it("maps a through ref to a flow source keyed on its node id", () => {
    expect(flowRefToSource({ kind: "through", nodeId: "n1", entrypointId: "ep1" }, [])).toEqual({
      kind: "flow",
      id: "n1",
    });
  });

  it("maps a varflow ref to a flow source keyed on its node id", () => {
    expect(flowRefToSource({ kind: "varflow", nodeId: "n1" }, [])).toEqual({ kind: "flow", id: "n1" });
  });

  it("falls back to a group of the resolved chain's real hop ids for path refs, excluding the synthetic seam-channel pill", () => {
    const chains: FlowChain[] = [
      { hops: [{ nodeId: "a", label: "A", service: "s" }, { nodeId: "seam-channel:e1", label: "ch", service: "channel" }, { nodeId: "b", label: "B", service: "s" }] },
    ];
    const source = flowRefToSource({ kind: "path", from: "a", to: "b", index: 0 }, chains);
    expect(source.kind).toBe("group");
    expect(source.kind === "group" && source.ids.sort()).toEqual(["a", "b"]);
  });

  it("falls back to a group for waypoints/edgeset/pins refs the same way", () => {
    const chains: FlowChain[] = [{ hops: [{ nodeId: "x", label: "X", service: "s" }] }];
    expect(flowRefToSource({ kind: "waypoints", ids: ["x"], direction: "forward" }, chains)).toEqual({
      kind: "group",
      ids: ["x"],
    });
    expect(flowRefToSource({ kind: "edgeset", nodeId: "x", edgeTypes: ["calls"] }, chains)).toEqual({
      kind: "group",
      ids: ["x"],
    });
    expect(flowRefToSource({ kind: "pins", ids: ["x"] }, chains)).toEqual({ kind: "group", ids: ["x"] });
  });
});

describe("selectionCopySource", () => {
  it("prefers a real single selection", () => {
    const source = selectionCopySource({ kind: "node", id: "n1" }, new Set(), undefined);
    expect(source).toEqual({ kind: "node", id: "n1" });
  });

  it("rejects a synthetic aggregation/rollup selection and falls through", () => {
    expect(selectionCopySource({ kind: "edge", id: "agg:a->b:calls" }, new Set(), undefined)).toBeNull();
    expect(selectionCopySource({ kind: "edge", id: "rollup:x" }, new Set(), undefined)).toBeNull();
  });

  it("rejects a service-aggregate node selection", () => {
    expect(selectionCopySource({ kind: "node", id: "service:auth" }, new Set(), undefined)).toBeNull();
  });

  it("falls back to a live multi-select of 2+ ids", () => {
    const source = selectionCopySource(null, new Set(["a", "b"]), undefined);
    expect(source).toEqual({ kind: "group", ids: ["a", "b"] });
  });

  it("ignores a multi-select of fewer than 2 ids", () => {
    expect(selectionCopySource(null, new Set(["a"]), undefined)).toBeNull();
  });

  it("falls back to the committed group scope", () => {
    const source = selectionCopySource(null, new Set(), { kind: "group", nodeIds: ["a", "b"] });
    expect(source).toEqual({ kind: "group", ids: ["a", "b"] });
  });

  it("falls back to the committed flow scope", () => {
    const source = selectionCopySource(null, new Set(), { kind: "flow", flow: { kind: "seam", edgeId: "e1" } });
    expect(source).toEqual({ kind: "edge", id: "e1" });
  });

  it("returns null when there's nothing selected or scoped", () => {
    expect(selectionCopySource(null, new Set(), undefined)).toBeNull();
    expect(selectionCopySource(null, new Set(), { kind: "overview" })).toBeNull();
  });
});
