import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import DeadcodePanel from "./DeadcodePanel";
import { deadcodeStore, type DeadcodeResult } from "../../stores/deadcode";
import { selectionStore } from "../../stores/selection";

function fixture(overrides: Partial<DeadcodeResult> = {}): DeadcodeResult {
  return {
    total: 2,
    functions: [
      { id: "be:orphan", label: "orphan", type: "function", service: "backend", file: "orphan.go", line: 30 },
      { id: "be:contained", label: "contained", type: "function", service: "backend", file: "contained.go", line: 5 },
    ],
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

describe("DeadcodePanel", () => {
  let container: HTMLElement;

  beforeEach(() => {
    deadcodeStore.reset();
    selectionStore.setSelection(null);
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    container.remove();
    deadcodeStore.reset();
    selectionStore.setSelection(null);
    vi.restoreAllMocks();
  });

  it("renders the zero-caller list from the fixture", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/deadcode": fixture(),
      "/api/stack": { services: [{ name: "backend", language: "go", files: 1 }] },
    });
    render(() => <DeadcodePanel />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="deadcode-row"]')).toHaveLength(2));
    expect(container.querySelector('[data-testid="deadcode-total"]')?.textContent).toContain("2 dead-code candidates");
  });

  it("renders a DC.27 Rails-view (file-type) row alongside function rows", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/deadcode": fixture({
        total: 1,
        functions: [
          {
            id: "be:app/views/shared/_orphan.html.erb:file",
            label: "app/views/shared/_orphan.html.erb",
            type: "file",
            service: "backend",
            file: "app/views/shared/_orphan.html.erb",
            line: 0,
          },
        ],
      }),
      "/api/stack": { services: [{ name: "backend", language: "ruby", files: 1 }] },
    });
    render(() => <DeadcodePanel />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="deadcode-row"]')).toHaveLength(1));
    expect(container.querySelector('[data-testid="deadcode-total"]')?.textContent).toContain("1 dead-code candidate");
    expect(container.querySelector('[data-testid="deadcode-row"]')?.textContent).toContain("file");
  });

  it("renders the empty-scope fallback when total is 0", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/deadcode": fixture({ total: 0, functions: [] }),
      "/api/stack": { services: [] },
    });
    render(() => <DeadcodePanel />, container);

    await vi.waitFor(() => expect(container.querySelector('[data-testid="deadcode-total"]')).toBeTruthy());
    expect(container.querySelectorAll('[data-testid="deadcode-row"]')).toHaveLength(0);
    expect(container.textContent).toContain("none found");
  });

  it("clicking a row selects the node", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/deadcode": fixture(),
      "/api/stack": { services: [] },
    });
    render(() => <DeadcodePanel />, container);

    await vi.waitFor(() => expect(container.querySelectorAll('[data-testid="deadcode-row"]')).toHaveLength(2));
    (container.querySelectorAll('[data-testid="deadcode-row"]')[0] as HTMLElement).click();

    expect(selectionStore.selection()).toEqual({ kind: "node", id: "be:orphan" });
  });
});
