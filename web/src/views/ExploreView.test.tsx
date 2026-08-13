import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import ExploreView from "./ExploreView";
import { exploreStore } from "../stores/explore";
import { treeStore } from "../stores/tree";
import { layoutPrefs } from "../stores/layoutPrefs";

function fakeFetch() {
  return vi.fn(() => Promise.resolve({ ok: true, json: async () => ({ services: [] }) } as Response));
}

describe("ExploreView", () => {
  let container: HTMLElement;
  let dispose: (() => void) | undefined;

  beforeEach(() => {
    exploreStore.reset();
    treeStore.reset();
    (globalThis as any).fetch = fakeFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    dispose = render(() => <ExploreView />, container);
  });

  afterEach(() => {
    dispose?.();
    container.remove();
  });

  it("defaults to the Tree tab and switches to Stack on click", () => {
    expect(container.querySelector('[data-testid="stack-panel"]')).toBeFalsy();

    (container.querySelector('[data-testid="explore-tab-stack"]') as HTMLElement).click();

    expect(exploreStore.tab()).toBe("stack");
    expect(container.querySelector('[data-testid="stack-panel"]')).toBeTruthy();
  });

  it("widens a too-narrow panel on switching to Stack, but never re-fights a manual resize back down", () => {
    layoutPrefs.setPanelWidth(280);

    (container.querySelector('[data-testid="explore-tab-stack"]') as HTMLElement).click();
    expect(layoutPrefs.panelWidth()).toBe(420);

    layoutPrefs.setPanelWidth(300); // user resizes back down while still on Stack
    expect(layoutPrefs.panelWidth()).toBe(300); // not clobbered back to 420
  });

  it("does not widen an already-wide panel", () => {
    layoutPrefs.setPanelWidth(600);
    (container.querySelector('[data-testid="explore-tab-stack"]') as HTMLElement).click();
    expect(layoutPrefs.panelWidth()).toBe(600);
  });
});
