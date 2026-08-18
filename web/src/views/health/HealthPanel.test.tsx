import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import HealthPanel from "./HealthPanel";
import { healthStore, type HealthData } from "../../stores/health";
import { drawerStore } from "../../stores/drawer";

function fixture(overrides: Partial<HealthData> = {}): HealthData {
  return {
    index: {
      indexed_at: "2026-08-18T00:00:00Z",
      schema_version: "31",
      nodes: 100,
      edges: 200,
      parse_errors: 1,
      parse_error_list: [
        { file_path: "auth/user.go", service: "auth", error_count: 2, first_error_line: 10, indexed_at: 1 },
      ],
    },
    coverage: { verified: 10, candidate: 5, observed_only_gap: 2, conflicting: 1 },
    unresolved_total: 3,
    unresolved_by_kind: { call: 2, import: 1 },
    eval: { present: false },
    trust: { measured: false },
    ...overrides,
  };
}

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const entry = routes[u.pathname];
    if (!entry) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => entry } as Response);
  });
}

describe("HealthPanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    healthStore.reset();
    drawerStore.setOpen(false);
    drawerStore.setActiveTab("context");
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    healthStore.reset();
    vi.restoreAllMocks();
  });

  it("renders the fixture exactly, including the eval present:false branch", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/health": fixture() });
    render(() => <HealthPanel />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="health-panel"]')).toBeTruthy());
    await vi.waitFor(() => expect(container.querySelector('[data-testid="health-indexed-at"]')).toBeTruthy());

    expect(container.querySelector('[data-testid="health-indexed-at"]')?.textContent).toBe("2026-08-18T00:00:00Z");
    expect(container.querySelector('[data-testid="health-coverage-row-verified"]')?.textContent).toContain("10");
    expect(container.querySelectorAll('[data-testid="health-coverage-row-verified"], [data-testid="health-coverage-row-candidate"], [data-testid="health-coverage-row-observed_only_gap"], [data-testid="health-coverage-row-conflicting"]')).toHaveLength(4);
    expect(container.querySelectorAll('[data-testid="health-unresolved-kind-row"]')).toHaveLength(2);
    expect(container.querySelector('[data-testid="health-eval-empty"]')?.textContent).toContain("no eval baseline found");
    expect(container.querySelectorAll('[data-testid="health-parse-error-row"]')).toHaveLength(1);
  });

  it("renders the eval present:true branch as a recall table", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/health": fixture({ eval: { present: true, repos: [{ name: "chessleap", recall: 0.875 }] } }),
    });
    render(() => <HealthPanel />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="health-eval-row"]')).toBeTruthy());
    expect(container.querySelector('[data-testid="health-eval-row"]')?.textContent).toContain("87.5%");
    expect(container.querySelector('[data-testid="health-eval-empty"]')).toBeFalsy();
  });

  it("clicking an unresolved kind row opens the drawer's Unresolved tab filtered to that kind", async () => {
    (globalThis as any).fetch = fakeFetch({ "/api/health": fixture() });
    render(() => <HealthPanel />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="health-unresolved-kind-row"]')).toHaveLength(2));
    (container.querySelectorAll('[data-testid="health-unresolved-kind-row"]')[0] as HTMLElement).click();

    expect(drawerStore.open()).toBe(true);
    expect(drawerStore.activeTab()).toBe("unresolved");
    expect(drawerStore.unresolvedKindFilter()).toBe("call");
  });
});
