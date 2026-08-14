import { describe, it, expect, vi } from "vitest";
import { resolveGroup } from "./group";

const ALL_GRAPH = {
  nodes: [
    { data: { id: "n1", label: "handler", type: "function", service: "rails", file: "app/h.rb", line: 1, language: "ruby" } },
    { data: { id: "n2", label: "template", type: "file", service: "rails", file: "app/t.erb", line: 0, language: "erb" } },
    { data: { id: "n3", label: "store", type: "function", service: "rails", file: "app/s.rb", line: 1, language: "ruby" } },
    { data: { id: "n4", label: "unrelated", type: "function", service: "rails", file: "app/u.rb", line: 1, language: "ruby" } },
  ],
  edges: [
    { data: { id: "e1", source: "n1", target: "n2", type: "renders" } },
    { data: { id: "e2", source: "n1", target: "n3", type: "calls" } },
    // Touches an unselected node — must not appear in the induced subgraph.
    { data: { id: "e3", source: "n1", target: "n4", type: "calls" } },
  ],
};

function fakeFetch() {
  return vi.fn(() => Promise.resolve({ ok: true, json: async () => ALL_GRAPH } as Response));
}

describe("resolveGroup", () => {
  it("returns exactly the selected nodes", async () => {
    (globalThis as any).fetch = fakeFetch();
    const d = await resolveGroup({ kind: "group", nodeIds: ["n1", "n2", "n3"] });
    expect(d.nodes.map((n) => n.id)).toEqual(["n1", "n2", "n3"]);
  });

  it("keeps only edges with both endpoints inside the selection (induced subgraph)", async () => {
    (globalThis as any).fetch = fakeFetch();
    const d = await resolveGroup({ kind: "group", nodeIds: ["n1", "n2", "n3"] });
    expect(d.edges.map((e) => e.id).sort()).toEqual(["e1", "e2"]);
  });

  it("drops an edge to a node outside the selection (negative)", async () => {
    (globalThis as any).fetch = fakeFetch();
    const d = await resolveGroup({ kind: "group", nodeIds: ["n1", "n2", "n3"] });
    expect(d.edges.find((e) => e.id === "e3")).toBeUndefined();
  });

  it("is deterministic across two runs", async () => {
    (globalThis as any).fetch = fakeFetch();
    const a = await resolveGroup({ kind: "group", nodeIds: ["n3", "n1", "n2"] });
    const b = await resolveGroup({ kind: "group", nodeIds: ["n1", "n2", "n3"] });
    expect(a).toEqual(b);
  });
});
