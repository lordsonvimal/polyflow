import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import Catalog, { filterAndSort } from "./Catalog";
import { scopeStore } from "../../stores/scope";
import { commands } from "../../commands/registry";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const ITEMS = [
  { node_id: "b", kind: "route", label: "GET /users", service: "b-svc", file: "b.rb", line: 1, channel: "GET /users" },
  { node_id: "a", kind: "subscriber", label: "orders.created", service: "a-svc", file: "a.rb", line: 2 },
  { node_id: "c", kind: "route", label: "POST /orders", service: "c-svc", file: "c.rb", line: 3 },
];

function entrypointsBody(skipped: { type: string; count: number }[] = []) {
  return { entrypoints: ITEMS, skipped };
}

describe("filterAndSort (pure)", () => {
  const parsed = ITEMS.map((i) => ({
    nodeId: i.node_id,
    kind: i.kind,
    label: i.label,
    service: i.service,
    file: i.file,
    line: i.line,
  }));

  it("filters by kind", () => {
    const out = filterAndSort(parsed, "", "subscriber", "service");
    expect(out.map((i) => i.nodeId)).toEqual(["a"]);
  });

  it("filters by free text across label/service/file", () => {
    expect(filterAndSort(parsed, "orders", null, "service").map((i) => i.nodeId).sort()).toEqual(["a", "c"]);
  });

  it("sorts deterministically by the chosen key, tie-broken by nodeId", () => {
    const out1 = filterAndSort(parsed, "", null, "label");
    const out2 = filterAndSort(parsed, "", null, "label");
    expect(out1.map((i) => i.nodeId)).toEqual(out2.map((i) => i.nodeId));
    expect(out1.map((i) => i.label)).toEqual(["GET /users", "orders.created", "POST /orders"]);
  });
});

describe("Catalog", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  it("registers a Flows group command in the palette registry", () => {
    expect(commands().some((c) => c.id === "flows:catalog")).toBe(true);
  });

  it("renders every fetched entrypoint as a row", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/entrypoints": entrypointsBody() });
    render(() => <Catalog />, container);

    await vi.waitFor(() => {
      expect(container.querySelectorAll('[data-testid="catalog-row"]').length).toBe(3);
    });
  });

  it("kind filter chip narrows the visible rows", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/entrypoints": entrypointsBody() });
    render(() => <Catalog />, container);

    const chip = await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="catalog-kind-route"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    chip.click();

    await vi.waitFor(() => {
      expect(container.querySelectorAll('[data-testid="catalog-row"]').length).toBe(2);
    });
  });

  it("row click isolates that entrypoint's own forward flow as a lane", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/entrypoints": entrypointsBody() });
    render(() => <Catalog />, container);

    const row = await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="catalog-row"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    row.click();

    expect(scopeStore.stack().at(-1)).toMatchObject({ kind: "flow" });
  });

  it("footer shows the honest skipped total and expands to a per-type breakdown", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/entrypoints": entrypointsBody([
        { type: "callback", count: 312 },
        { type: "unreachable", count: 41 },
      ]),
    });
    render(() => <Catalog />, container);

    const toggle = await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="catalog-skipped-toggle"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(toggle.textContent).toContain("353 not listed");
    expect(container.querySelector('[data-testid="catalog-skipped-detail"]')).toBeNull();

    toggle.click();

    const detail = container.querySelector('[data-testid="catalog-skipped-detail"]') as HTMLElement;
    expect(detail).toBeTruthy();
    expect(detail.textContent).toContain("312 callback");
    expect(detail.textContent).toContain("41 unreachable");
  });

  it("empty result renders the honest empty state, not a blank list", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/entrypoints": entrypointsBody() });
    render(() => <Catalog />, container);

    const search = await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="catalog-search"]');
      expect(el).toBeTruthy();
      return el as HTMLInputElement;
    });
    search.value = "no-such-entrypoint";
    search.dispatchEvent(new Event("input", { bubbles: true }));

    await vi.waitFor(() => expect(container.querySelector('[data-testid="catalog-empty"]')).toBeTruthy());
  });
});
