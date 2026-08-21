import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ServicePairPanel from "./ServicePairPanel";
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

describe("ServicePairPanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  it("renders one row per channel, kind/label/verification badge and producer/consumer counts", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/services/channels?from=rails-svc&to=cdr-svc": {
        from: "rails-svc",
        to: "cdr-svc",
        channels: [
          { kind: "publishes", channel: "cdr_requests", edge_id: "e1", from: "rails-svc", to: "cdr-svc", verification_state: "verified", producer_count: 1, consumer_count: 2 },
          { kind: "http_call", channel: "GET /status", edge_id: "e2", from: "cdr-svc", to: "rails-svc", verification_state: "candidate", producer_count: 3, consumer_count: 1 },
        ],
      },
    });
    render(() => <ServicePairPanel from="rails-svc" to="cdr-svc" />, container);

    const rows = await vi.waitFor(() => {
      const r = container.querySelectorAll('[data-testid="service-pair-channel-row"]');
      expect(r.length).toBe(2);
      return [...r];
    });

    expect(rows[0].textContent).toContain("cdr_requests");
    expect(rows[0].textContent).toContain("verified");
    expect(rows[0].textContent).toContain("1 producer");
    expect(rows[0].textContent).toContain("2 consumers");
    expect(rows[0].textContent).toContain("rails-svc → cdr-svc");
    expect(rows[1].textContent).toContain("GET /status");
    expect(rows[1].textContent).toContain("cdr-svc → rails-svc");
  });

  it("clicking a channel row pushes its seam isolation", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/services/channels?from=a&to=b": {
        from: "a",
        to: "b",
        channels: [{ kind: "publishes", channel: "x", edge_id: "e-target", producer_count: 1, consumer_count: 1 }],
      },
    });
    render(() => <ServicePairPanel from="a" to="b" />, container);

    const row = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="service-pair-channel-row"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });
    row.click();

    const top = scopeStore.stack().at(-1);
    expect(top).toEqual({ kind: "flow", flow: { kind: "seam", edgeId: "e-target" } });
  });

  it("shows an honest empty state when nothing crosses the pair", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/services/channels?from=a&to=b": { from: "a", to: "b", channels: [] },
    });
    render(() => <ServicePairPanel from="a" to="b" />, container);

    await vi.waitFor(() => {
      expect(container.querySelector('[data-testid="service-pair-panel-empty"]')).toBeTruthy();
    });
  });
});
