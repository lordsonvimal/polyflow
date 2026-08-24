import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import FleetSwitcher from "./FleetSwitcher";
import { fleetMembersStore, type FleetMemberRow } from "../../stores/fleetMembers";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string, init?: RequestInit) => {
    const u = new URL(url, "http://localhost");
    if (init?.method === "POST" && u.pathname === "/api/fleet/active") {
      const body = JSON.parse(String(init.body)) as { service: string };
      routes["__lastActive"] = body.service;
      return Promise.resolve({ ok: true, json: async () => ({ active: body.service }) } as Response);
    }
    const entry = routes[u.pathname];
    if (!entry) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("FleetSwitcher", () => {
  let container: HTMLElement;

  beforeEach(() => {
    fleetMembersStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    fleetMembersStore.reset();
    vi.restoreAllMocks();
  });

  it("renders nothing when this workspace isn't a fleet member", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/fleet/services": { services: [] } });
    render(() => <FleetSwitcher />, container);

    await vi.waitFor(() => expect(fleetMembersStore.loading()).toBe(false));
    expect(container.querySelector('[data-testid="fleet-switcher"]')).toBeNull();
  });

  it("lists every member, showing which are already resolved/merged", async () => {
    const services: FleetMemberRow[] = [
      { service: "api", active: true },
      { service: "web", active: false },
    ];
    (globalThis as any).fetch = fakeFetch({ "/api/fleet/services": { services } });
    render(() => <FleetSwitcher />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="fleet-switcher-row"]')).toHaveLength(2));

    expect(container.querySelectorAll('[data-testid="fleet-switcher-loaded"]')).toHaveLength(1);
    expect(container.querySelectorAll('[data-testid="fleet-switcher-load"]')).toHaveLength(1);
  });

  it("loading an unresolved member widens the merge rather than switching away from others", async () => {
    const services: FleetMemberRow[] = [
      { service: "api", active: true },
      { service: "web", active: false },
    ];
    const routes: Record<string, unknown> = { "/api/fleet/services": { services } };
    (globalThis as any).fetch = fakeFetch(routes);
    render(() => <FleetSwitcher />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="fleet-switcher-load"]')).toHaveLength(1));
    const loadBtn = container.querySelector('[data-testid="fleet-switcher-load"]') as HTMLButtonElement;
    loadBtn.click();

    await vi.waitFor(() => expect(routes["__lastActive"]).toBe("web"));
    await vi.waitFor(() => expect(fleetMembersStore.services().find((s) => s.service === "web")?.active).toBe(true));
    // Loading "web" must not have deactivated "api" — the whole fleet stays merged.
    expect(fleetMembersStore.services().find((s) => s.service === "api")?.active).toBe(true);
  });
});
