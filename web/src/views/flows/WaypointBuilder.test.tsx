import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import WaypointBuilder from "./WaypointBuilder";
import { waypointBuilderStore } from "../../stores/waypointBuilder";
import { scopeStore } from "../../stores/scope";

function fakeFetch(routes: Record<string, unknown>) {
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

describe("WaypointBuilder", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    scopeStore.reset();
    waypointBuilderStore.clear();
    waypointBuilderStore.consumeSeed();
    waypointBuilderStore.setDirection("forward");
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("renders the seed chip and candidate lists, live-pushing the flow scope", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/refine?waypoints=seed&direction=forward": {
        chain: [{ node_id: "seed", label: "Seed", service: "svc" }],
        candidates: {
          upstream: [{ node_id: "up1", label: "Up1", service: "svc", type: "function", via_edge_type: "calls" }],
          downstream: [{ node_id: "down1", label: "Down1", service: "svc", type: "function", via_edge_type: "calls" }],
        },
      },
    });
    waypointBuilderStore.requestStart({ id: "seed", label: "Seed" });
    dispose = render(() => <WaypointBuilder />, container);

    const chips = container.querySelectorAll('[data-testid="waypoint-chip"]');
    expect(chips.length).toBe(1);
    expect(chips[0].textContent).toContain("Seed");

    await vi.waitFor(() => {
      expect(container.querySelector('[data-testid="waypoint-candidate-upstream"]')).toBeTruthy();
      expect(container.querySelector('[data-testid="waypoint-candidate-downstream"]')).toBeTruthy();
    });

    await vi.waitFor(() =>
      expect(scopeStore.stack().at(-1)).toEqual({
        kind: "flow",
        flow: { kind: "waypoints", ids: ["seed"], direction: "forward" },
      }),
    );
  });

  it("clicking a downstream candidate appends it and re-queries, updating the lane in place (no stack growth)", async () => {
    let call = 0;
    (globalThis as any).fetch = vi.fn((url: string) => {
      call++;
      const u = new URL(url, "http://localhost");
      if (u.search.includes("waypoints=seed&direction=forward")) {
        return Promise.resolve({
          ok: true,
          json: async () => ({
            chain: [{ node_id: "seed", label: "Seed", service: "svc" }],
            candidates: {
              upstream: [],
              downstream: [{ node_id: "down1", label: "Down1", service: "svc", type: "function", via_edge_type: "calls" }],
            },
          }),
        } as Response);
      }
      if (u.search.includes("waypoints=seed%2Cdown1&direction=forward") || u.search.includes("waypoints=seed,down1&direction=forward")) {
        return Promise.resolve({
          ok: true,
          json: async () => ({
            chain: [
              { node_id: "seed", label: "Seed", service: "svc" },
              { node_id: "down1", label: "Down1", service: "svc" },
            ],
            candidates: { upstream: [], downstream: [] },
          }),
        } as Response);
      }
      return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    });

    waypointBuilderStore.requestStart({ id: "seed", label: "Seed" });
    dispose = render(() => <WaypointBuilder />, container);

    const downstreamRow = await vi.waitFor(() => {
      const r = container.querySelector('[data-testid="waypoint-candidate-downstream"]');
      expect(r).toBeTruthy();
      return r as HTMLElement;
    });
    const stackLengthBefore = scopeStore.stack().length;
    downstreamRow.click();

    await vi.waitFor(() => {
      const chips = container.querySelectorAll('[data-testid="waypoint-chip"]');
      expect(chips.length).toBe(2);
    });
    expect(scopeStore.stack().length).toBe(stackLengthBefore);
    expect(scopeStore.stack().at(-1)).toEqual({
      kind: "flow",
      flow: { kind: "waypoints", ids: ["seed", "down1"], direction: "forward" },
    });
    expect(call).toBeGreaterThanOrEqual(2);
  });

  it("removing a chip that disconnects the remainder shows an inline error naming the pair, keeping chips for editing", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/refine?waypoints=a%2Cmid%2Cb&direction=forward": {
        chain: [
          { node_id: "a", label: "A", service: "svc" },
          { node_id: "mid", label: "Mid", service: "svc" },
          { node_id: "b", label: "B", service: "svc" },
        ],
        candidates: { upstream: [], downstream: [] },
      },
      "/api/flows/refine?waypoints=a%2Cb&direction=forward": { status: 422, body: JSON.stringify({ error: "waypoints not connected: a -> b" }) },
    });
    waypointBuilderStore.requestStart({ id: "a", label: "A" });
    waypointBuilderStore.append({ id: "mid", label: "Mid" });
    waypointBuilderStore.append({ id: "b", label: "B" });
    dispose = render(() => <WaypointBuilder />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="waypoint-chip"]').length).toBe(3));

    const removeButtons = container.querySelectorAll('[data-testid="waypoint-chip-remove"]');
    (removeButtons[1] as HTMLElement).click(); // remove "mid"

    await vi.waitFor(() => {
      const err = container.querySelector('[data-testid="waypoint-error"]');
      expect(err).toBeTruthy();
      expect(err!.textContent).toContain("a -> b");
    });
    // Chips kept for editing — the removal itself is not rolled back.
    expect(container.querySelectorAll('[data-testid="waypoint-chip"]').length).toBe(2);
  });

  it("clear empties the session and pops the pushed flow scope", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/flows/refine?waypoints=seed&direction=forward": {
        chain: [{ node_id: "seed", label: "Seed", service: "svc" }],
        candidates: { upstream: [], downstream: [] },
      },
    });
    waypointBuilderStore.requestStart({ id: "seed", label: "Seed" });
    dispose = render(() => <WaypointBuilder />, container);

    await vi.waitFor(() => expect(scopeStore.stack().at(-1)?.kind).toBe("flow"));
    const stackLenWithFlow = scopeStore.stack().length;

    const clearBtn = container.querySelector('[data-testid="waypoint-clear"]') as HTMLElement;
    clearBtn.click();

    expect(waypointBuilderStore.waypoints()).toEqual([]);
    expect(scopeStore.stack().length).toBe(stackLenWithFlow - 1);
  });
});
