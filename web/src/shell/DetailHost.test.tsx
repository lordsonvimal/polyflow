import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import DetailHost from "./DetailHost";
import { selectionStore } from "../stores/selection";
import { exploreStore } from "../stores/explore";
import { layoutPrefs } from "../stores/layoutPrefs";
import { serviceNodeId } from "../lib/aggregate";
import { flowsThroughStore } from "../stores/flowsThrough";
import { servicePairStore } from "../stores/servicePair";
import { scopeStore } from "../stores/scope";

describe("DetailHost", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    selectionStore.setSelection(null);
    exploreStore.reset();
    layoutPrefs.setActivity("flows");
    scopeStore.reset();
    (globalThis as any).fetch = vi.fn(() => Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response));
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <DetailHost />, container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
    scopeStore.reset();
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

  it("a real edge selection renders its seam summary (UF.3)", () => {
    selectionStore.setSelection({ kind: "edge", id: "e-p1-ch1" });
    expect(container.querySelector('[data-testid="seam-summary"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="service-pair-panel"]')).toBeFalsy();
  });

  it("an aggregated overview pill selection (servicePairStore bridge) renders the channel drill-in, not the seam summary", () => {
    servicePairStore.open("rails-svc", "cdr-svc", "agg:rails-svc->cdr-svc:publishes");
    selectionStore.setSelection({ kind: "edge", id: "agg:rails-svc->cdr-svc:publishes" });

    expect(container.querySelector('[data-testid="service-pair-panel"]')).toBeTruthy();
    expect(container.querySelector('[data-testid="seam-summary"]')).toBeFalsy();
    servicePairStore.close();
  });

  it("a group scope renders the group summary (UF.4), independent of node selection", () => {
    scopeStore.push({ kind: "group", nodeIds: ["n1", "n2"] });
    expect(container.querySelector('[data-testid="group-summary"]')).toBeTruthy();
    expect(container.textContent).toContain("2 selected — Group");
  });
});
