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

  it("lists every member with the active one selected, and switches on change", async () => {
    const services: FleetMemberRow[] = [
      { service: "api", active: true },
      { service: "web", active: false },
    ];
    const routes: Record<string, unknown> = { "/api/fleet/services": { services } };
    (globalThis as any).fetch = fakeFetch(routes);
    render(() => <FleetSwitcher />, container);

    await vi.waitFor(() => expect(container.querySelectorAll("option")).toHaveLength(2));

    const select = container.querySelector('[data-testid="fleet-switcher-select"]') as HTMLSelectElement;
    expect(select.value).toBe("api");

    select.value = "web";
    select.dispatchEvent(new Event("change"));

    await vi.waitFor(() => expect(routes["__lastActive"]).toBe("web"));
    await vi.waitFor(() => expect(fleetMembersStore.services().find((s) => s.service === "web")?.active).toBe(true));
    expect(fleetMembersStore.services().find((s) => s.service === "api")?.active).toBe(false);
  });
});
