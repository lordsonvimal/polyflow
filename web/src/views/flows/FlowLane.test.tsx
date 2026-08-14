import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import FlowLane from "./FlowLane";
import { scopeStore } from "../../stores/scope";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

const CHAIN = [
  { node_id: "root", label: "POST /orders", service: "rails-svc" },
  { node_id: "leaf", label: "publish", service: "rails-svc" },
];

function throughBody(truncated: boolean) {
  return {
    flows: [{ entrypoint: { node_id: "root", kind: "route", label: "POST /orders", service: "rails-svc" }, chain: CHAIN }],
    truncated,
  };
}

describe("FlowLane", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  it("renders the entrypoint → terminus chip from the resolved chain", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/leaf?limit=20": throughBody(false) });
    scopeStore.push({ kind: "flow", flow: { kind: "through", nodeId: "leaf", entrypointId: "root" } });
    render(() => <FlowLane />, container);

    await vi.waitFor(() => expect(container.textContent).toContain("POST /orders → publish"));
  });

  it("shows the truncation cap when the chain list is truncated, and re-queries with a larger limit on click", async () => {
    const fetchMock = fakeFetch({
      "/api/flows/through/leaf?limit=20": throughBody(true),
      "/api/flows/through/leaf?limit=40": throughBody(false),
    });
    (globalThis as any).fetch = fetchMock;
    scopeStore.push({ kind: "flow", flow: { kind: "through", nodeId: "leaf", entrypointId: "root" } });
    render(() => <FlowLane />, container);

    const cap = await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="flow-truncation-cap"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });

    cap.click();

    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("limit=40"), expect.anything());
    });
    await vi.waitFor(() => expect(container.querySelector('[data-testid="flow-truncation-cap"]')).toBeNull());
  });

  it("[×] pops back to the prior scope, restoring the cached viewport slot", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/flows/through/leaf?limit=20": throughBody(false) });
    scopeStore.push({ kind: "service", service: "rails-svc" });
    scopeStore.push({ kind: "flow", flow: { kind: "through", nodeId: "leaf", entrypointId: "root" } });
    render(() => <FlowLane />, container);

    await vi.waitFor(() => expect(container.textContent).toContain("Flow:"));

    const closeBtn = [...container.querySelectorAll("button")].find((b) => b.textContent === "×")!;
    closeBtn.click();

    expect(scopeStore.stack().map((s) => s.kind)).toEqual(["overview", "service"]);
  });

  it("renders the honest unreachable message for a no-path result", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=a&to=b&k=20": { paths: [], reachable: false },
    });
    scopeStore.push({ kind: "flow", flow: { kind: "path", from: "a", to: "b", index: 0 } });
    render(() => <FlowLane />, container);

    await vi.waitFor(() => expect(container.textContent).toContain("No static path"));
  });
});
