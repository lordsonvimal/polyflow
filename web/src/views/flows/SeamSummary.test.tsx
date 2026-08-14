import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import SeamSummary from "./SeamSummary";
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

describe("SeamSummary", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  it("renders channel key, verification state, evidence sources and producer/consumer counts", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/seam/e1": {
        channel: "rabbitmq:cdr_requests",
        verification_state: "verified",
        expanded: true,
        sources: [{ provider: "static", confidence: "declared" }],
        producers: [{ node: { id: "p1" }, chain: [] }],
        consumers: [
          { node: { id: "c1" }, chain: [] },
          { node: { id: "c2" }, chain: [] },
        ],
      },
    });
    render(() => <SeamSummary edgeId="e1" />, container);

    await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="seam-summary"]') as HTMLElement;
      expect(el.textContent).toContain("rabbitmq:cdr_requests");
      expect(el.textContent).toContain("verified");
      expect(el.textContent).toContain("1 producer");
      expect(el.textContent).toContain("2 consumers");
      expect(el.textContent).toContain("static");
    });
    expect(container.querySelector('[data-testid="seam-summary-no-closure"]')).toBeFalsy();
  });

  it("shows an honest 'no channel closure' note when the edge kind couldn't expand", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/seam/e2": {
        channel: "calls",
        expanded: false,
        producers: [{ node: { id: "a" }, chain: [] }],
        consumers: [{ node: { id: "b" }, chain: [] }],
      },
    });
    render(() => <SeamSummary edgeId="e2" />, container);

    await vi.waitFor(() => {
      expect(container.querySelector('[data-testid="seam-summary-no-closure"]')).toBeTruthy();
    });
  });

  it("isolate button pushes the seam flow scope", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/seam/e3": { channel: "x", expanded: true, producers: [], consumers: [] },
    });
    render(() => <SeamSummary edgeId="e3" />, container);

    const btn = await vi.waitFor(() => {
      const b = container.querySelector('[data-testid="seam-summary-isolate"]') as HTMLElement;
      expect(b).toBeTruthy();
      return b;
    });
    btn.click();

    expect(scopeStore.stack().at(-1)).toEqual({ kind: "flow", flow: { kind: "seam", edgeId: "e3" } });
  });
});
