import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import DetailHost from "./DetailHost";
import { selectionStore } from "../stores/selection";
import { exploreStore } from "../stores/explore";
import { layoutPrefs } from "../stores/layoutPrefs";
import { serviceNodeId } from "../lib/aggregate";
import { flowsThroughStore } from "../stores/flowsThrough";

describe("DetailHost", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    selectionStore.setSelection(null);
    exploreStore.reset();
    layoutPrefs.setActivity("flows");
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response));
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <DetailHost />, container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("a service pseudo-node selection shows a 'View in Tech Stack' link instead of a source panel (UN.4)", () => {
    selectionStore.setSelection({ kind: "node", id: serviceNodeId("railssvc") });
    const link = container.querySelector('[data-testid="detail-view-in-stack"]') as HTMLElement;
    expect(link).toBeTruthy();
    expect(container.querySelector('[data-testid="source-panel"]')).toBeFalsy();

    link.click();

    expect(layoutPrefs.activity()).toBe("explore");
    expect(exploreStore.tab()).toBe("stack");
    expect(exploreStore.focusService()).toBe("railssvc");
  });

  it("a regular node selection shows the source panel, not the tech-stack link", () => {
    selectionStore.setSelection({ kind: "node", id: "auth:app/user.rb:method:save:5" });
    expect(container.querySelector('[data-testid="detail-view-in-stack"]')).toBeFalsy();
  });

  it("the 'Isolate flows through here' toggle is collapsed by default and expands the through panel on click", () => {
    selectionStore.setSelection({ kind: "node", id: "auth:app/user.rb:method:save:5" });
    expect(container.querySelector('[data-testid="through-panel"]')).toBeFalsy();

    const toggle = container.querySelector('[data-testid="detail-isolate-flows-through"]') as HTMLElement;
    expect(toggle).toBeTruthy();
    toggle.click();

    expect(container.querySelector('[data-testid="through-panel"]')).toBeTruthy();
  });

  it("a context-menu 'Isolate flows through here' request (flowsThroughStore) auto-expands the panel for that node", () => {
    selectionStore.setSelection({ kind: "node", id: "auth:app/user.rb:method:save:5" });
    expect(container.querySelector('[data-testid="through-panel"]')).toBeFalsy();

    flowsThroughStore.request("auth:app/user.rb:method:save:5");

    expect(container.querySelector('[data-testid="through-panel"]')).toBeTruthy();
    expect(flowsThroughStore.requestedNodeId()).toBeNull();
  });
});
