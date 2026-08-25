import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import LensBar, { displayedLens } from "./LensBar";
import { commands } from "../../commands/registry";
import { scopeStore } from "../../stores/scope";
import { LENS_NAMES } from "./lenses";
import { EDGE_GROUP_NAMES } from "../../lib/edgeGroups";

describe("displayedLens", () => {
  it("shows the active lens when FilterBar's edgeType chips are untouched", () => {
    expect(displayedLens({ edgeTypes: [] }, "Calls")).toBe("Calls");
    expect(displayedLens({ edgeTypes: [...EDGE_GROUP_NAMES] }, "HTTP")).toBe("HTTP");
  });

  it("defaults to All when no lens has ever been set", () => {
    expect(displayedLens({ edgeTypes: [] }, undefined)).toBe("All");
  });

  it("switches to Custom when a chip has been toggled off", () => {
    const narrowed = EDGE_GROUP_NAMES.filter((g) => g !== "dom");
    expect(displayedLens({ edgeTypes: narrowed }, "Calls")).toBe("Custom");
  });
});

describe("palette commands", () => {
  it("registers 'Switch lens: <name>' for every lens", () => {
    const ids = commands().map((c) => c.id);
    for (const name of LENS_NAMES) {
      expect(ids).toContain(`lens:${name}`);
    }
  });

  it("running a lens command sets scopeStore's lens", () => {
    commands().find((c) => c.id === "lens:HTTP")!.run();
    expect(scopeStore.viewState().lens).toBe("HTTP");
  });
});

describe("LensBar", () => {
  let container: HTMLElement;

  beforeEach(() => {
    scopeStore.reset();
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => container.remove());

  function openLens(): void {
    (container.querySelector('[data-testid="lens-bar-toggle"]') as HTMLElement).click();
  }

  it("renders every lens name as a button behind the Lens toggle", () => {
    render(() => <LensBar />, container);
    openLens();
    const labels = [...document.querySelectorAll("button")].map((b) => b.textContent);
    for (const name of LENS_NAMES) expect(labels).toContain(name);
  });

  it("clicking a lens button switches the active lens", () => {
    render(() => <LensBar />, container);
    openLens();
    const btn = [...document.querySelectorAll("button")].find((b) => b.textContent === "Messaging")!;
    (btn as HTMLElement).click();
    expect(scopeStore.viewState().lens).toBe("Messaging");
  });

  it("only shows the rollup toggle under the Imports lens", () => {
    render(() => <LensBar />, container);
    openLens();
    expect(document.querySelector('[data-testid="lens-rollup"]')).toBeNull();
    scopeStore.setLens("Imports");
    expect(document.querySelector('[data-testid="lens-rollup"]')).toBeTruthy();
  });

  it("toggling hide-unlinked flips ViewState.lensHideUnlinked", () => {
    render(() => <LensBar />, container);
    openLens();
    const btn = document.querySelector('[data-testid="lens-hide-unlinked"]') as HTMLElement;
    btn.click();
    expect(scopeStore.viewState().lensHideUnlinked).toBe(true);
    btn.click();
    expect(scopeStore.viewState().lensHideUnlinked).toBe(false);
  });
});
