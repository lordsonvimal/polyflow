import { describe, it, expect } from "vitest";
import { checkBudget, autoCluster, layoutOptions, BUDGET } from "./budget";
import { GraphNode, GraphEdge } from "../../lib/types";

function node(id: string, service = "svc", file = "src/a.ts"): GraphNode {
  return { id, type: "function", label: id, service, file, line: 1, language: "ts" };
}

function edge(id: string, from = "x", to = "y"): GraphEdge {
  return { id, from, to, type: "calls" };
}

describe("checkBudget", () => {
  it("ok when under budget", () => {
    const r = checkBudget(
      Array.from({ length: 10 }, (_, i) => node(`n${i}`)),
      Array.from({ length: 5 }, (_, i) => edge(`e${i}`)),
    );
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.nodeCount).toBe(10);
      expect(r.edgeCount).toBe(5);
    }
  });

  it("ok at exactly BUDGET", () => {
    const nodes = Array.from({ length: 1000 }, (_, i) => node(`n${i}`));
    const edges = Array.from({ length: 500 }, (_, i) => edge(`e${i}`));
    expect(checkBudget(nodes, edges).ok).toBe(true);
  });

  it("over when nodes+edges exceed BUDGET", () => {
    const nodes = Array.from({ length: 1000 }, (_, i) => node(`n${i}`));
    const edges = Array.from({ length: 600 }, (_, i) => edge(`e${i}`));
    const r = checkBudget(nodes, edges);
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.total).toBe(1600);
      expect(r.nodeCount).toBe(1000);
      expect(r.edgeCount).toBe(600);
    }
  });

  it("produces per-child narrowing counts sorted ascending", () => {
    const nodes = [
      ...Array.from({ length: 900 }, (_, i) => node(`a${i}`, "svc-a")),
      ...Array.from({ length: 700 }, (_, i) => node(`b${i}`, "svc-b")),
    ];
    const r = checkBudget(nodes, []);
    expect(r.ok).toBe(false);
    if (!r.ok) {
      const a = r.children.find((c) => c.key === "svc-a")!;
      const b = r.children.find((c) => c.key === "svc-b")!;
      expect(a.count).toBe(900);
      expect(b.count).toBe(700);
      // sorted ascending: svc-b (700) < svc-a (900)
      expect(r.children[0].key).toBe("svc-b");
      expect(r.children[1].key).toBe("svc-a");
    }
  });

  it("uses custom groupKey", () => {
    const nodes = [
      node("n1", "svc", "folder-a/x.ts"),
      node("n2", "svc", "folder-a/y.ts"),
      ...Array.from({ length: 1500 }, (_, i) => node(`n${i + 3}`, "svc", `folder-b/z${i}.ts`)),
    ];
    const r = checkBudget(nodes, [], (n) => n.file.split("/")[0]);
    expect(r.ok).toBe(false);
    if (!r.ok) {
      const fa = r.children.find((c) => c.key === "folder-a")!;
      expect(fa.count).toBe(2);
    }
  });
});

describe("autoCluster", () => {
  it("returns original when already under budget", () => {
    const nodes = Array.from({ length: 5 }, (_, i) => node(`n${i}`));
    const r = autoCluster(nodes, []);
    expect(r.nodes.length + r.edges.length).toBeLessThanOrEqual(BUDGET);
  });

  it("collapses files until under budget", () => {
    // 20 files × 100 nodes = 2000 nodes, all same service
    const nodes: GraphNode[] = [];
    for (let f = 0; f < 20; f++) {
      for (let i = 0; i < 100; i++) {
        nodes.push({ id: `f${f}n${i}`, type: "function", label: `fn${i}`, service: "svc", file: `src/file${f}.ts`, line: i, language: "ts" });
      }
    }
    const edges: GraphEdge[] = [];
    for (let i = 0; i < 200; i++) {
      edges.push({ id: `e${i}`, from: nodes[i].id, to: nodes[(i + 1) % nodes.length].id, type: "calls" });
    }
    const r = autoCluster(nodes, edges);
    expect(r.nodes.length + r.edges.length).toBeLessThanOrEqual(BUDGET);
  });

  it("collapsed group nodes carry member count in label", () => {
    const nodes: GraphNode[] = [];
    for (let i = 0; i < 2000; i++) {
      nodes.push({ id: `n${i}`, type: "function", label: `fn${i}`, service: "svc", file: `src/file${i % 10}.ts`, line: i, language: "ts" });
    }
    const r = autoCluster(nodes, []);
    const groupNodes = r.nodes.filter((n) => /\(\d+\)/.test(n.label));
    expect(groupNodes.length).toBeGreaterThan(0);
    for (const g of groupNodes) {
      expect(g.label).toMatch(/\(\d+\)$/);
    }
  });
});

describe("layoutOptions", () => {
  it("uses dagre when preferred and no compounds", () => {
    const r = layoutOptions(false, "dagre", false);
    expect(r.name).toBe("dagre");
    expect(r.animate).toBe(true);
    expect(r.dagreDisabledReason).toBeUndefined();
  });

  it("falls back to fcose and sets reason when compounds present", () => {
    const r = layoutOptions(true, "dagre", false);
    expect(r.name).toBe("fcose");
    expect(r.dagreDisabledReason).toBeDefined();
    expect(r.dagreDisabledReason).toContain("compound");
  });

  it("uses fcose when preferred regardless of compounds", () => {
    const r = layoutOptions(true, "fcose", false);
    expect(r.name).toBe("fcose");
    expect(r.dagreDisabledReason).toBeUndefined();
  });

  it("suppresses animation when reduced-motion", () => {
    const r = layoutOptions(false, "fcose", true);
    expect(r.animate).toBe(false);
  });
});
