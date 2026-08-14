import { render } from "solid-js/web";
import { describe, it, expect, afterEach, vi } from "vitest";
import GroupSummary, { edgeTypeCounts, servicesTouched, containedFiles, sharedChannels, matrixCell } from "./GroupSummary";
import type { GraphEdge, GraphNode } from "../../lib/types";

function n(id: string, type: string, service: string, file: string): GraphNode {
  return { id, type, label: id, service, file, line: 1, language: "ruby" };
}
function e(id: string, from: string, to: string, type: string): GraphEdge {
  return { id, from, to, type };
}

describe("GroupSummary pure helpers", () => {
  it("edgeTypeCounts tallies by type", () => {
    const edges = [e("e1", "a", "b", "calls"), e("e2", "a", "c", "calls"), e("e3", "b", "c", "renders")];
    expect(edgeTypeCounts(edges)).toEqual(new Map([["calls", 2], ["renders", 1]]));
  });

  it("servicesTouched dedupes and sorts", () => {
    const nodes = [n("a", "function", "svcB", "x"), n("b", "function", "svcA", "y"), n("c", "function", "svcB", "z")];
    expect(servicesTouched(nodes)).toEqual(["svcA", "svcB"]);
  });

  it("containedFiles dedupes and sorts, dropping empty file paths", () => {
    const nodes = [n("a", "function", "svc", "b.rb"), n("b", "function", "svc", "a.rb"), n("c", "function", "svc", "")];
    expect(containedFiles(nodes)).toEqual(["a.rb", "b.rb"]);
  });

  it("sharedChannels returns only channel-typed nodes", () => {
    const nodes = [n("a", "function", "svc", "x"), n("ch1", "channel", "svc", "")];
    expect(sharedChannels(nodes)).toEqual(["ch1"]);
  });

  it("matrixCell glyphs the edge types between two nodes, either direction", () => {
    const edges = [e("e1", "a", "b", "calls"), e("e2", "b", "a", "flows_to")];
    expect(matrixCell(edges, "a", "b")).toBe("C·FT");
  });

  it("matrixCell is empty for an unconnected pair", () => {
    const edges = [e("e1", "a", "b", "calls")];
    expect(matrixCell(edges, "a", "c")).toBe("");
  });
});

const SMALL_GRAPH = {
  nodes: [
    { data: { id: "n1", label: "handler", type: "function", service: "rails", file: "app/h.rb", line: 1, language: "ruby" } },
    { data: { id: "n2", label: "template", type: "file", service: "rails", file: "app/t.erb", line: 0, language: "erb" } },
    { data: { id: "n3", label: "store", type: "function", service: "rails", file: "app/s.rb", line: 1, language: "ruby" } },
  ],
  edges: [
    { data: { id: "e1", source: "n1", target: "n2", type: "renders" } },
    { data: { id: "e2", source: "n1", target: "n3", type: "calls" } },
  ],
};

describe("GroupSummary component", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  function setup(nodeIds: string[]) {
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, json: async () => SMALL_GRAPH } as Response));
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <GroupSummary nodeIds={nodeIds} />, container);
  }

  it("shows the interconnection matrix for groups of 8 nodes or fewer", async () => {
    setup(["n1", "n2", "n3"]);
    await vi.waitFor(() => expect(container.querySelector('[data-testid="group-matrix"]')).toBeTruthy());
  });

  it("hides the matrix above the 8-node gate", async () => {
    const bigIds = Array.from({ length: 9 }, (_, i) => `n${i}`);
    const bigGraph = {
      nodes: bigIds.map((id) => ({ data: { id, label: id, type: "function", service: "rails", file: "app/x.rb", line: 1, language: "ruby" } })),
      edges: [],
    };
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: true, json: async () => bigGraph } as Response));
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <GroupSummary nodeIds={bigIds} />, container);
    await vi.waitFor(() => expect(container.querySelector('[data-testid="group-summary"]')?.textContent).toContain("9 nodes"));
    expect(container.querySelector('[data-testid="group-matrix"]')).toBeFalsy();
  });

  it("renders edge-type counts and services touched", async () => {
    setup(["n1", "n2", "n3"]);
    await vi.waitFor(() => expect(container.textContent).toContain("renders × 1"));
    expect(container.textContent).toContain("calls × 1");
    expect(container.textContent).toContain("rails");
  });
});
