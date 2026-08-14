import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ThroughPanel from "./ThroughPanel";
import { scopeStore } from "../../stores/scope";
import { flowHighlightStore } from "../../stores/flowHighlight";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const CHAIN_A = [
  { node_id: "root-a", label: "POST /orders", service: "rails-svc" },
  { node_id: "mid", label: "OrdersController#create", service: "rails-svc" },
  { node_id: "target", label: "publish", service: "rails-svc" },
];

const CHAIN_B = [
  { node_id: "root-b", label: "subscribe orders.created", service: "consumer-svc", verification_state: "candidate" },
  { node_id: "target", label: "publish", service: "rails-svc" },
];

function body(truncated = false) {
  return {
    flows: [
      { entrypoint: { node_id: "root-a", kind: "route", label: "POST /orders", service: "rails-svc" }, chain: CHAIN_A },
      { entrypoint: { node_id: "root-b", kind: "subscriber", label: "subscribe orders.created", service: "consumer-svc" }, chain: CHAIN_B },
    ],
    truncated,
  };
}

describe("ThroughPanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    flowHighlightStore.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  it("renders one row per entrypoint group, with hop count/services/verification badge", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/target?limit=20": body() });
    render(() => <ThroughPanel nodeId="target" />, container);

    const rows = await vi.waitFor(() => {
      const r = container.querySelectorAll('[data-testid="through-panel-row"]');
      expect(r.length).toBe(2);
      return [...r];
    });

    expect(rows[0].textContent).toContain("POST /orders");
    expect(rows[0].textContent).toContain("3 hops");
    expect(rows[0].textContent).toContain("rails-svc");
    expect(rows[0].textContent).toContain("verified");

    expect(rows[1].textContent).toContain("subscribe orders.created");
    expect(rows[1].textContent).toContain("2 hops");
    expect(rows[1].textContent).toContain("consumer-svc, rails-svc");
    expect(rows[1].textContent).toContain("candidate");
  });

  it("hover pre-highlights the group's member ids and clears on mouse-leave, without a layout call", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/target?limit=20": body() });
    render(() => <ThroughPanel nodeId="target" />, container);

    const row = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="through-panel-row"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });

    row.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
    expect([...flowHighlightStore.ids()].sort()).toEqual(["mid", "root-a", "target"]);

    row.dispatchEvent(new MouseEvent("mouseleave", { bubbles: true }));
    expect(flowHighlightStore.ids().size).toBe(0);
  });

  it("clicking a row isolates that entrypoint's flow as a lane", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/target?limit=20": body() });
    render(() => <ThroughPanel nodeId="target" />, container);

    const row = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="through-panel-row"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });
    row.click();

    expect(scopeStore.stack().at(-1)).toEqual({
      kind: "flow",
      flow: { kind: "through", nodeId: "target", entrypointId: "root-a" },
    });
  });

  it("renders the honest empty state when no flows pass through the node", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/lonely?limit=20": { flows: [], truncated: false } });
    render(() => <ThroughPanel nodeId="lonely" />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="through-panel-empty"]')).toBeTruthy());
  });
});
