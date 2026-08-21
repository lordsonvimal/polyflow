import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import FleetStatusPanel from "./FleetStatusPanel";
import { fleetStatusStore, type FleetServiceStatus } from "../../stores/fleetStatus";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const entry = routes[u.pathname];
    if (!entry) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("FleetStatusPanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    fleetStatusStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    fleetStatusStore.reset();
    vi.restoreAllMocks();
  });

  it("renders one row per service, distinguishing indexed from never-indexed", async () => {
    const services: FleetServiceStatus[] = [
      { service: "api", indexed_at: "2026-08-18T00:00:00Z", node_count: 42, edge_count: 17, indexed: true },
      { service: "web", node_count: 0, edge_count: 0, indexed: false },
    ];
    (globalThis as any).fetch = fakeFetch({ "/api/fleet/status": { services } });
    render(() => <FleetStatusPanel />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="fleet-status-row"]')).toHaveLength(2));

    const rows = container.querySelectorAll('[data-testid="fleet-status-row"]');
    expect(rows[0].textContent).toContain("api");
    expect(rows[0].textContent).toContain("42");
    expect(rows[1].textContent).toContain("web");
    expect(rows[1].textContent).toContain("never indexed on its own");
  });

  it("renders an empty state when no services are configured", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/fleet/status": { services: [] } });
    render(() => <FleetStatusPanel />, container);

    await vi.waitFor(() => expect(container.textContent).toContain("No services configured."));
  });
});
