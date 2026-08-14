import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import PathFinderPanel from "./PathFinderPanel";
import { scopeStore } from "../../stores/scope";
import { flowHighlightStore } from "../../stores/flowHighlight";
import { pathOverlayStore } from "../../stores/pathOverlay";
import { drawerStore } from "../../stores/drawer";

function fakeFetch(routes: Record<string, unknown | { status: number; body: string }>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    const entry = routes[match] as { status: number; body: string } | unknown;
    if (entry && typeof entry === "object" && "status" in (entry as object) && "body" in (entry as object)) {
      const e = entry as { status: number; body: string };
      return Promise.resolve({ ok: false, status: e.status, text: async () => e.body } as Response);
    }
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

const HOP_A = { node_id: "a", label: "A", service: "svc" };
const HOP_MID = { node_id: "mid", label: "Mid", service: "svc" };
const HOP_B = { node_id: "b", label: "B", service: "svc" };

describe("PathFinderPanel", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    scopeStore.reset();
    flowHighlightStore.clear();
    pathOverlayStore.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("ranks paths by hop count then worst confidence, deterministically", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=a&to=b&k=20": {
        reachable: true,
        paths: [
          { chain: [HOP_A, { ...HOP_MID, confidence: "partial" }, HOP_B] }, // 2 hops, worst=partial
          { chain: [HOP_A, HOP_B] }, // 1 hop
        ],
      },
    });
    dispose = render(() => <PathFinderPanel from="a" fromLabel="A" to="b" />, container);

    const rows = await vi.waitFor(() => {
      const r = container.querySelectorAll('[data-testid="path-finder-row"]');
      expect(r.length).toBe(2);
      return [...r];
    });
    // Shortest (1 hop) ranks first even though it was second in backend order.
    expect(rows[0].textContent).toContain("1 hop");
    expect(rows[1].textContent).toContain("2 hops");
    expect(rows[1].textContent).toContain("partial");
  });

  it("hover previews dim non-members via flowHighlightStore", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=a&to=b&k=20": { reachable: true, paths: [{ chain: [HOP_A, HOP_B] }] },
    });
    dispose = render(() => <PathFinderPanel from="a" fromLabel="A" to="b" />, container);

    const row = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="path-finder-row"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });
    row.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
    expect([...flowHighlightStore.ids()].sort()).toEqual(["a", "b"]);
    row.dispatchEvent(new MouseEvent("mouseleave", { bubbles: true }));
    expect(flowHighlightStore.ids().size).toBe(0);
  });

  it("clicking a row isolates that path by its backend index (not display rank)", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=a&to=b&k=20": {
        reachable: true,
        paths: [
          { chain: [HOP_A, HOP_MID, HOP_B] }, // index 0, 2 hops
          { chain: [HOP_A, HOP_B] }, // index 1, 1 hop -> displays first
        ],
      },
    });
    dispose = render(() => <PathFinderPanel from="a" fromLabel="A" to="b" />, container);

    const row = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="path-finder-row"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });
    row.click();

    expect(scopeStore.stack().at(-1)).toEqual({
      kind: "flow",
      flow: { kind: "path", from: "a", to: "b", index: 1 },
    });
  });

  it("Overlay all assigns a distinct color group per ranked path, >5 grouped", async () => {
    const paths = Array.from({ length: 7 }, (_, i) => ({ chain: [HOP_A, { node_id: `m${i}`, label: `M${i}`, service: "svc" }, HOP_B] }));
    (globalThis as any).fetch = fakeFetch({ "/api/flows/paths?from=a&to=b&k=20": { reachable: true, paths } });
    dispose = render(() => <PathFinderPanel from="a" fromLabel="A" to="b" />, container);

    const toggle = await vi.waitFor(() => {
      const t = container.querySelector('[data-testid="path-finder-overlay-toggle"]');
      expect(t).toBeTruthy();
      return t as HTMLElement;
    });
    toggle.click();

    const assignment = pathOverlayStore.assignment();
    expect(assignment.get("m4")).toBe(4);
    expect(assignment.get("m5")).toBe(5);
    expect(assignment.get("m6")).toBe(5);

    toggle.click();
    expect(pathOverlayStore.assignment().size).toBe(0);
  });

  it("renders an honest unreachable state with nearest-entrypoint info and an unresolved-check link", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/paths?from=a&to=b&k=20": { reachable: false, paths: [] },
      "/api/flows/through/a?limit=5": { flows: [{ entrypoint: { label: "GET /a-entry" } }], truncated: false },
      "/api/flows/through/b?limit=5": { flows: [], truncated: false },
      "/api/node/a": { node: { service: "svc", file: "app/a.rb" } },
    });
    dispose = render(() => <PathFinderPanel from="a" fromLabel="A" to="b" />, container);

    await vi.waitFor(() => {
      const el = container.querySelector('[data-testid="path-finder-unreachable"]');
      expect(el).toBeTruthy();
      expect(el!.textContent).toContain("No static path");
    });
    await vi.waitFor(() => expect(container.textContent).toContain("GET /a-entry"));

    const link = container.querySelector('[data-testid="path-finder-check-unresolved-from"]') as HTMLElement;
    link.click();
    await vi.waitFor(() => expect(drawerStore.unresolvedFilter()).toEqual({ service: "svc", path: "app/a.rb" }));
  });
});
