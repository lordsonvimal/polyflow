import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import LinkExplorer from "./LinkExplorer";
import { scopeStore } from "../../stores/scope";
import { selectionStore } from "../../stores/selection";
import { flowHighlightStore } from "../../stores/flowHighlight";
import { canvasElementsStore } from "../../stores/canvasElements";
import { expandedElementsStore } from "../../stores/expandedElements";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

function counts(up: number, down: number) {
  return {
    "/api/node/target/links?direction=upstream&depth=1&offset=0&limit=1": { direction: "upstream", depth: 1, total: up, offset: 0, rows: [], truncated: false },
    "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=1": { direction: "downstream", depth: 1, total: down, offset: 0, rows: [], truncated: false },
  };
}

const ROW_A = {
  node_id: "n1", label: "validateOrder", type: "function", service: "svc-a", file: "orders.go", line: 30,
  edge_id: "e1", edge_type: "calls", depth: 1,
};
const ROW_B = {
  node_id: "n2", label: "chargeCard", type: "http_handler", service: "svc-b", file: "billing.go", line: 10,
  edge_id: "e2", edge_type: "calls", depth: 1, verification_state: "candidate",
};

describe("LinkExplorer", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    selectionStore.setSelection(null);
    flowHighlightStore.clear();
    canvasElementsStore.setIds(new Set());
    expandedElementsStore.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  it("renders downstream rows by default, with header counts for both directions", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(3, 2),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 2, offset: 0, rows: [ROW_A, ROW_B], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    const rows = await vi.waitFor(() => {
      const r = container.querySelectorAll('[data-testid="link-explorer-row"]');
      expect(r.length).toBe(2);
      return [...r];
    });
    expect(rows[0].textContent).toContain("validateOrder");
    expect(rows[1].textContent).toContain("chargeCard");

    await vi.waitFor(() => {
      expect(container.querySelector('[data-testid="link-explorer-upstream"]')?.textContent).toContain("3");
      expect(container.querySelector('[data-testid="link-explorer-downstream"]')?.textContent).toContain("2");
    });
  });

  it("switches to upstream on toggle click", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(1, 0),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 0, offset: 0, rows: [], truncated: false,
      },
      "/api/node/target/links?direction=upstream&depth=1&offset=0&limit=100": {
        direction: "upstream", depth: 1, total: 1, offset: 0, rows: [ROW_A], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="link-explorer-empty"]')).toBeTruthy());

    (container.querySelector('[data-testid="link-explorer-upstream"]') as HTMLElement).click();

    await vi.waitFor(() => {
      const r = container.querySelectorAll('[data-testid="link-explorer-row"]');
      expect(r.length).toBe(1);
    });
  });

  it("hover peeks with flowHighlightStore (ViewState untouched) and clears on mouse-leave", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 1),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 1, offset: 0, rows: [ROW_A], truncated: false,
      },
    });
    const before = scopeStore.viewState();
    render(() => <LinkExplorer nodeId="target" />, container);

    const row = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="link-explorer-row"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });

    row.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
    expect([...flowHighlightStore.ids()].sort()).toEqual(["n1", "target"]);
    expect(scopeStore.viewState()).toBe(before);

    row.dispatchEvent(new MouseEvent("mouseleave", { bubbles: true }));
    expect(flowHighlightStore.ids().size).toBe(0);
  });

  it("commit-expand adds exactly the row's node+edge to scope.expanded and the element cache", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 1),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 1, offset: 0, rows: [ROW_A], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    const btn = await vi.waitFor(() => {
      const b = container.querySelector('[data-testid="link-explorer-commit-expand"]');
      expect(b).toBeTruthy();
      return b as HTMLElement;
    });
    btn.click();

    expect(scopeStore.viewState().expanded).toEqual(["n1"]);
    const cached = expandedElementsStore.entries().get("n1");
    expect(cached?.node.label).toBe("validateOrder");
    expect(cached?.edge).toEqual({ id: "e1", from: "target", to: "n1", type: "calls", label: undefined });
  });

  it("commit-expand no-ops when the canvas is already at budget", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 1),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 1, offset: 0, rows: [ROW_A], truncated: false,
      },
    });
    canvasElementsStore.setIds(new Set(Array.from({ length: 1500 }, (_, i) => `n${i}`)));
    render(() => <LinkExplorer nodeId="target" />, container);

    const btn = await vi.waitFor(() => {
      const b = container.querySelector('[data-testid="link-explorer-commit-expand"]');
      expect(b).toBeTruthy();
      return b as HTMLElement;
    });
    btn.click();

    expect(scopeStore.viewState().expanded ?? []).toEqual([]);
  });

  it("commit-navigate pushes the row's file scope, selects it, and reveals it in the tree", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 1),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 1, offset: 0, rows: [ROW_A], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    const btn = await vi.waitFor(() => {
      const b = container.querySelector('[data-testid="link-explorer-commit-navigate"]');
      expect(b).toBeTruthy();
      return b as HTMLElement;
    });
    btn.click();

    expect(scopeStore.stack().at(-1)).toEqual({ kind: "file", service: "svc-a", path: "orders.go" });
    expect(selectionStore.selection()).toEqual({ kind: "node", id: "n1" });
  });

  it("filters the loaded list client-side with kind:/service: chip syntax", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 2),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 2, offset: 0, rows: [ROW_A, ROW_B], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="link-explorer-row"]').length).toBe(2));

    const input = container.querySelector('[data-testid="link-explorer-filter"]') as HTMLInputElement;
    input.value = "kind:http_handler";
    input.dispatchEvent(new Event("input", { bubbles: true }));

    await vi.waitFor(() => {
      const rows = container.querySelectorAll('[data-testid="link-explorer-row"]');
      expect(rows.length).toBe(1);
      expect(rows[0].textContent).toContain("chargeCard");
    });
  });

  it("groups depth>1 rows under a via heading", async () => {
    const deepRow = { ...ROW_A, node_id: "n3", label: "deepFn", depth: 2, via: ["validateOrder"] };
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 1),
      "/api/node/target/links?direction=downstream&depth=2&offset=0&limit=100": {
        direction: "downstream", depth: 2, total: 1, offset: 0, rows: [deepRow], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    const depthSelect = container.querySelector('[data-testid="link-explorer-depth"]') as HTMLSelectElement;
    depthSelect.value = "2";
    depthSelect.dispatchEvent(new Event("change", { bubbles: true }));

    await vi.waitFor(() => {
      expect(container.textContent).toContain("via validateOrder");
      expect(container.textContent).toContain("deepFn");
    });
  });

  it("renders the honest empty state when a direction has no links", async () => {
    (globalThis as any).fetch = fakeFetch({
      ...counts(0, 0),
      "/api/node/target/links?direction=downstream&depth=1&offset=0&limit=100": {
        direction: "downstream", depth: 1, total: 0, offset: 0, rows: [], truncated: false,
      },
    });
    render(() => <LinkExplorer nodeId="target" />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="link-explorer-empty"]')).toBeTruthy());
  });
});
