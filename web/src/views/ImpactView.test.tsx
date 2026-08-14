import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ImpactView from "./ImpactView";
import { scopeStore } from "../stores/scope";

function fakeFetch(routes: Record<string, unknown>) {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    const key = u.pathname + u.search;
    const match = Object.keys(routes).find((k) => key === k);
    if (!match) return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    return Promise.resolve({ ok: true, json: async () => routes[match] } as Response);
  });
}

describe("ImpactView / Impact tab (rings)", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("prompts for a target when no impact scope is active", () => {
    dispose = render(() => <ImpactView />, container);
    expect(container.textContent).toContain('Impact from here');
  });

  it("shows direction/depth controls for the active impact scope and re-queries on change", () => {
    scopeStore.push({ kind: "impact", target: "n1", direction: "both", depth: 3 });
    dispose = render(() => <ImpactView />, container);

    expect(container.querySelector('[data-testid="impact-depth"]')).toBeTruthy();

    (container.querySelector('[data-testid="impact-direction-up"]') as HTMLElement).click();
    const top = scopeStore.stack().at(-1) as any;
    expect(top.direction).toBe("up");
    expect(top.target).toBe("n1"); // replaceTop, not push — stack length unchanged
    expect(scopeStore.stack().length).toBe(2);

    const depthInput = container.querySelector('[data-testid="impact-depth"]') as HTMLInputElement;
    depthInput.value = "7";
    // Solid delegates "input" via a document-level listener — the event
    // must bubble for it to be observed.
    depthInput.dispatchEvent(new Event("input", { bubbles: true }));
    expect((scopeStore.stack().at(-1) as any).depth).toBe(7);
  });
});

describe("ImpactView / Diff tab", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("renders changed nodes badged M, the union blast radius, and unmapped hunks (never dropped)", async () => {
    (globalThis as any).fetch = fakeFetch({
      "/api/impact/diff": {
        mode: "worktree",
        depth: 10,
        changed_files: 1,
        targets: [{ node: { id: "n1", label: "Handler", file: "svc/a.go", line: 4 }, changed_spans: [{ start: 4, end: 4 }] }],
        unmapped_hunks: [{ file: "svc/b.go", reason: "no node overlaps this span" }],
        callers: [{ id: "n2", label: "Caller", file: "svc/c.go", line: 20, depth: 1, edge_type: "calls" }],
        services_affected: ["svc"],
        total_callers: 1,
      },
    });

    dispose = render(() => <ImpactView />, container);
    (container.querySelector('[data-testid="impact-tab-diff"]') as HTMLElement).click();

    await vi.waitFor(() => expect(container.querySelector('[data-testid="diff-targets"]')).toBeTruthy());

    expect(container.querySelector('[data-testid="diff-target-badge"]')!.textContent).toBe("M");
    expect(container.querySelector('[data-testid="diff-targets"]')!.textContent).toContain("Handler");
    expect(container.querySelector('[data-testid="diff-callers"]')!.textContent).toContain("Caller");
    expect(container.querySelector('[data-testid="diff-unmapped"]')!.textContent).toContain("no node overlaps this span");
  });
});
