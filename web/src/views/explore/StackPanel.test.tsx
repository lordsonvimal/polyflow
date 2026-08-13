import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import StackPanel, { computeTotals, crossServiceChannelCounts } from "./StackPanel";
import { treeStore, type ServiceSummary } from "../../stores/tree";
import { paletteStore } from "../../stores/palette";
import { scopeStore } from "../../stores/scope";
import { exploreStore } from "../../stores/explore";
import type { GraphNode, GraphEdge } from "../../lib/types";

const RAILSSVC = {
  name: "railssvc",
  language: "ruby",
  frameworks: ["rails"],
  files: 42,
  deps: [
    { name: "rails", version: "7.1.0", ecosystem: "rubygems" },
    { name: "sidekiq", version: "7.2.0", ecosystem: "rubygems" },
  ],
  node_counts: { http_handler: 34, method: 120 },
  edge_counts: { calls: 500, http_call: 12 },
};

const GOSVCB = {
  name: "go-svcb",
  language: "go",
  frameworks: [],
  files: 10,
  deps: [],
  node_counts: { function: 40 },
  edge_counts: { calls: 80 },
};

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k || key.startsWith(k));
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

describe("computeTotals", () => {
  it("sums files/nodes/edges across every service", () => {
    const services: ServiceSummary[] = [
      { name: "a", language: "go", frameworks: [], files: 5, deps: [], nodeCounts: { function: 3 }, edgeCounts: { calls: 2 } },
      { name: "b", language: "ruby", frameworks: [], files: 7, deps: [], nodeCounts: { method: 4, class: 1 }, edgeCounts: { calls: 1 } },
    ];
    expect(computeTotals(services)).toEqual({ services: 2, files: 12, nodes: 8, edges: 3 });
  });

  it("empty workspace totals to all zeros", () => {
    expect(computeTotals([])).toEqual({ services: 0, files: 0, nodes: 0, edges: 0 });
  });
});

describe("crossServiceChannelCounts", () => {
  it("groups cross-service edges by coarse edge-type group, summed and sorted desc", () => {
    const nodes: GraphNode[] = [
      { id: "n1", type: "function", label: "n1", service: "a", file: "", line: 0, language: "" },
      { id: "n2", type: "function", label: "n2", service: "b", file: "", line: 0, language: "" },
      { id: "n3", type: "function", label: "n3", service: "b", file: "", line: 0, language: "" },
    ];
    const edges: GraphEdge[] = [
      { id: "e1", from: "n1", to: "n2", type: "http_call" },
      { id: "e2", from: "n1", to: "n3", type: "http_call" },
      { id: "e3", from: "n1", to: "n2", type: "publishes" },
    ];
    expect(crossServiceChannelCounts(nodes, edges)).toEqual([
      { group: "http", count: 2 },
      { group: "messaging", count: 1 },
    ]);
  });

  it("same-service edges never count as cross-service", () => {
    const nodes: GraphNode[] = [
      { id: "n1", type: "function", label: "n1", service: "a", file: "", line: 0, language: "" },
      { id: "n2", type: "function", label: "n2", service: "a", file: "", line: 0, language: "" },
    ];
    const edges: GraphEdge[] = [{ id: "e1", from: "n1", to: "n2", type: "calls" }];
    expect(crossServiceChannelCounts(nodes, edges)).toEqual([]);
  });
});

describe("StackPanel", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    treeStore.reset();
    exploreStore.reset();
    scopeStore.reset();
    paletteStore.close();
    (globalThis as any).fetch = fakeFetch({
      "/api/stack": { services: [RAILSSVC, GOSVCB] },
      "/api/graph?limit=2000": { nodes: [], edges: [] },
    });
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  async function mount() {
    dispose = render(() => <StackPanel />, container);
    await vi.waitFor(() => expect(treeStore.services().length).toBe(2));
  }

  it("renders the fixture /api/stack response exactly: totals, per-service cards, deps, node/edge distributions", async () => {
    await mount();

    expect(container.querySelector('[data-testid="stack-header"]')?.textContent).toContain("2 services · 52 files · 194 nodes · 592 edges");

    const railsCard = container.querySelector('[data-testid="stack-card-railssvc"]') as HTMLElement;
    expect(railsCard.textContent).toContain("railssvc");
    expect(railsCard.textContent).toContain("ruby");
    expect(railsCard.textContent).toContain("rails");
    expect(railsCard.textContent).toContain("42 files");
    expect(railsCard.textContent).toContain("sidekiq");
    expect(railsCard.textContent).toContain("7.2.0");
    expect(railsCard.querySelector('[data-testid="stack-count-Nodes-http_handler"]')?.textContent).toBe("34");
    expect(railsCard.querySelector('[data-testid="stack-count-Edges-http_call"]')?.textContent).toBe("12");
  });

  it("empty-deps service renders honestly (\"no dependency manifest found\"), not blank", async () => {
    await mount();
    const goCard = container.querySelector('[data-testid="stack-card-go-svcb"]') as HTMLElement;
    expect(goCard.textContent).toContain("no dependency manifest found");
  });

  it("a node-count number click pre-fills the palette with kind:+service (UN.4 click-navigate)", async () => {
    await mount();
    const btn = container.querySelector('[data-testid="stack-count-Nodes-http_handler"]') as HTMLElement;
    btn.click();
    expect(paletteStore.isOpen()).toBe(true);
    expect(paletteStore.pendingQuery()).toBe("kind:http_handler service:railssvc");
  });

  it("a service's file count click pushes its service scope", async () => {
    await mount();
    const btn = container.querySelector('[data-testid="stack-files-railssvc"]') as HTMLElement;
    btn.click();
    expect(scopeStore.stack().at(-1)).toEqual({ kind: "service", service: "railssvc" });
  });

  it("no services indexed renders an honest empty state, not a blank page", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/stack": { services: [] },
      "/api/graph?limit=2000": { nodes: [], edges: [] },
    });
    dispose = render(() => <StackPanel />, container);
    await vi.waitFor(() => expect(container.querySelector('[data-testid="empty-state"]')).toBeTruthy());
    expect(container.textContent).toContain("No services indexed");
  });
});
