import { render } from "solid-js/web";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import FilterBar, { computeActiveCount, effectiveAllOn, effectiveConfidence } from "./FilterBar";
import { scopeStore } from "../../stores/scope";
import { treeStore } from "../../stores/tree";
import { EDGE_GROUP_NAMES } from "../../lib/edgeGroups";
import { canvasElementsStore } from "../../stores/canvasElements";
import { multiSelectStore } from "../../stores/multiSelect";
import { BUDGET } from "./budget";

function fakeFetch() {
  return vi.fn((url: string) => {
    const u = new URL(url, "http://localhost");
    if (u.pathname === "/api/stack") {
      return Promise.resolve({
        ok: true,
        json: async () => ({ services: [{ name: "svc1", language: "go", frameworks: [], files: 1 }, { name: "svc2", language: "ruby", frameworks: [], files: 1 }] }),
      } as Response);
    }
    return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
  });
}

describe("FilterBar", () => {
  let container: HTMLElement;

  beforeEach(async () => {
    treeStore.reset();
    scopeStore.reset();
    scopeStore.setFilters({ confidence: [], edgeTypes: [], services: [] });
    (globalThis as any).fetch = fakeFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    render(() => <FilterBar />, container);
    await vi.waitFor(() => expect(treeStore.services().length).toBe(2));
  });

  afterEach(() => container.remove());

  function chip(label: string): HTMLElement {
    return [...container.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLElement;
  }

  it("renders confidence, edge-group, and service chips", () => {
    expect(chip("static")).toBeTruthy();
    expect(chip("inferred")).toBeTruthy();
    expect(chip("partial")).toBeTruthy();
    expect(chip("unknown")).toBeTruthy();
    for (const g of EDGE_GROUP_NAMES) expect(chip(g)).toBeTruthy();
    expect(chip("svc1")).toBeTruthy();
    expect(chip("svc2")).toBeTruthy();
  });

  it("toggling an opt-in confidence tier adds it explicitly", () => {
    chip("partial").click();
    expect(scopeStore.viewState().filters.confidence.sort()).toEqual(["inferred", "partial", "static"].sort());
  });

  it("toggling off a default-on confidence tier removes it explicitly", () => {
    chip("inferred").click();
    expect(scopeStore.viewState().filters.confidence).toEqual(["static"]);
  });

  it("toggling off one edge-type group leaves the rest explicit", () => {
    chip("calls").click();
    const filters = scopeStore.viewState().filters;
    expect(filters.edgeTypes.includes("calls")).toBe(false);
    expect(filters.edgeTypes.length).toBe(EDGE_GROUP_NAMES.length - 1);
  });

  it("toggling every edge-type group back on collapses to [] (canonical all-on)", () => {
    chip("calls").click();
    chip("calls").click();
    expect(scopeStore.viewState().filters.edgeTypes).toEqual([]);
  });

  it("toggling a service off restricts to the rest", () => {
    chip("svc1").click();
    expect(scopeStore.viewState().filters.services).toEqual(["svc2"]);
  });

  it("reset clears all filters back to the default (empty) state", () => {
    chip("partial").click();
    chip("calls").click();
    chip("svc1").click();
    const resetBtn = [...container.querySelectorAll("button")].find((b) => b.textContent === "reset")!;
    resetBtn.click();
    expect(scopeStore.viewState().filters).toEqual({ confidence: [], edgeTypes: [], services: [], noiseClasses: [] });
  });
});

// Fleets with more than a handful of services collapse the services row
// into a single "Services (n/total) ▾" toggle + popover instead of one chip
// per service, so it can't crowd the fixed-size confidence/edge-type groups
// out of FilterBar's shared scroll strip.
describe("FilterBar - services overflow", () => {
  let container: HTMLElement;

  function manyServicesFetch() {
    return vi.fn((url: string) => {
      const u = new URL(url, "http://localhost");
      if (u.pathname === "/api/stack") {
        const names = ["svc1", "svc2", "svc3", "svc4", "svc5", "svc6"];
        return Promise.resolve({
          ok: true,
          json: async () => ({ services: names.map((name) => ({ name, language: "go", frameworks: [], files: 1 })) }),
        } as Response);
      }
      return Promise.resolve({ ok: false, status: 404, text: async () => "not found" } as Response);
    });
  }

  beforeEach(async () => {
    treeStore.reset();
    scopeStore.reset();
    scopeStore.setFilters({ confidence: [], edgeTypes: [], services: [] });
    (globalThis as any).fetch = manyServicesFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    render(() => <FilterBar />, container);
    await vi.waitFor(() => expect(treeStore.services().length).toBe(6));
  });

  afterEach(() => container.remove());

  function byTestId(id: string): HTMLElement {
    return container.querySelector(`[data-testid="${id}"]`) as HTMLElement;
  }

  it("collapses services into a toggle instead of one chip each", () => {
    expect(byTestId("filter-services-toggle")).toBeTruthy();
    expect(byTestId("filter-services-toggle").textContent).toBe("Services (6/6) ▾");
    expect(byTestId("filter-services-menu")).toBeFalsy();
  });

  it("opens a popover listing every service on click", () => {
    byTestId("filter-services-toggle").click();
    // The menu portals to <body> (outside `container`) so it isn't clipped
    // by the filter row's overflow-x-auto.
    const menu = document.querySelector('[data-testid="filter-services-menu"]')!;
    expect(menu).toBeTruthy();
    for (const name of ["svc1", "svc2", "svc3", "svc4", "svc5", "svc6"]) {
      expect([...menu.querySelectorAll("button")].some((b) => b.textContent === name)).toBe(true);
    }
  });

  it("toggling a service from the popover restricts the filter and updates the count", () => {
    byTestId("filter-services-toggle").click();
    const menu = document.querySelector('[data-testid="filter-services-menu"]')!;
    const svc1 = [...menu.querySelectorAll("button")].find((b) => b.textContent === "svc1")!;
    svc1.click();
    expect(scopeStore.viewState().filters.services).toEqual(["svc2", "svc3", "svc4", "svc5", "svc6"]);
    expect(byTestId("filter-services-toggle").textContent).toBe("Services (5/6) ▾");
  });
});

// Tier NV.7: the Noise row defaults every class off (opposite polarity from
// confidence/edgeTypes/services), matching the agent-side hide-by-default.
describe("FilterBar - Noise chip row", () => {
  let container: HTMLElement;

  beforeEach(async () => {
    treeStore.reset();
    scopeStore.reset();
    scopeStore.setFilters({ confidence: [], edgeTypes: [], services: [] });
    (globalThis as any).fetch = fakeFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    render(() => <FilterBar />, container);
    await vi.waitFor(() => expect(treeStore.services().length).toBe(2));
  });

  afterEach(() => container.remove());

  function chip(label: string): HTMLElement {
    return [...container.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLElement;
  }

  it("renders every noise-class chip inactive by default", () => {
    for (const label of ["Filter chains", "Mixins", "Containment", "Render tree"]) {
      const el = chip(label);
      expect(el).toBeTruthy();
      expect(el.className).not.toContain("bg-neutral-700");
    }
    expect(scopeStore.viewState().filters.noiseClasses ?? []).toEqual([]);
  });

  it("toggling a noise-class chip on adds just that class", () => {
    chip("Containment").click();
    expect(scopeStore.viewState().filters.noiseClasses).toEqual(["containment"]);
    chip("Mixins").click();
    expect(scopeStore.viewState().filters.noiseClasses).toEqual(["containment", "mixin"]);
    chip("Containment").click();
    expect(scopeStore.viewState().filters.noiseClasses).toEqual(["mixin"]);
  });

  it("shows a hidden-count badge sourced from canvasElementsStore.noiseHidden", () => {
    canvasElementsStore.setNoiseHidden(42);
    expect(container.textContent).toContain("Noise");
    expect(container.textContent).toContain("42 hidden");
  });
});

// UF.4: "Add all matches" unions the current on-canvas node set into the
// multi-selection (budget-checked), so it composes with the marquee HUD.
describe("FilterBar - Add all matches", () => {
  let container: HTMLElement;

  beforeEach(async () => {
    treeStore.reset();
    scopeStore.reset();
    multiSelectStore.clear();
    canvasElementsStore.setIds(new Set());
    (globalThis as any).fetch = fakeFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    render(() => <FilterBar />, container);
    await vi.waitFor(() => expect(treeStore.services().length).toBe(2));
  });

  afterEach(() => container.remove());

  function button(label: string): HTMLElement | undefined {
    return [...container.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLElement | undefined;
  }

  it("is hidden when nothing is on canvas", () => {
    expect(button("Add all matches")).toBeUndefined();
  });

  it("unions the on-canvas node ids into the multi-selection", () => {
    canvasElementsStore.setIds(new Set(["n1", "n2", "n3"]));
    button("Add all matches")!.click();
    expect([...multiSelectStore.ids()].sort()).toEqual(["n1", "n2", "n3"]);
  });

  it("caps the union at BUDGET and warns", () => {
    const many = new Set(Array.from({ length: BUDGET + 50 }, (_, i) => `n${i}`));
    canvasElementsStore.setIds(many);
    button("Add all matches")!.click();
    expect(multiSelectStore.ids().size).toBe(BUDGET);
  });
});

// UF.6: coverage overlay toggle — default on (undefined decodes as "on"),
// classes-only toggle (CanvasHost's own effect adds/removes the border
// class; this just flips the ViewState flag FilterBar reads).
describe("FilterBar - Coverage overlay toggle", () => {
  let container: HTMLElement;

  beforeEach(async () => {
    treeStore.reset();
    scopeStore.reset();
    (globalThis as any).fetch = fakeFetch();
    container = document.createElement("div");
    document.body.appendChild(container);
    render(() => <FilterBar />, container);
    await vi.waitFor(() => expect(treeStore.services().length).toBe(2));
  });

  afterEach(() => container.remove());

  function chip(label: string): HTMLElement {
    return [...container.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLElement;
  }

  it("is on by default", () => {
    expect(scopeStore.viewState().coverageOverlay).not.toBe(false);
  });

  it("toggles off then back on", () => {
    chip("Coverage").click();
    expect(scopeStore.viewState().coverageOverlay).toBe(false);
    chip("Coverage").click();
    expect(scopeStore.viewState().coverageOverlay).toBe(true);
  });
});

describe("computeActiveCount", () => {
  const allServices = ["svc1", "svc2"];

  it("is 0 for the fully-open default state", () => {
    expect(computeActiveCount({ confidence: [], edgeTypes: [], services: [] }, allServices)).toBe(0);
  });

  it("counts each confidence deviation from the default (static+inferred)", () => {
    expect(computeActiveCount({ confidence: ["static", "inferred", "partial"], edgeTypes: [], services: [] }, allServices)).toBe(1);
    expect(computeActiveCount({ confidence: ["static"], edgeTypes: [], services: [] }, allServices)).toBe(1);
  });

  it("counts turned-off edge-type groups and services", () => {
    expect(computeActiveCount({ confidence: [], edgeTypes: ["calls"], services: [] }, allServices)).toBe(EDGE_GROUP_NAMES.length - 1);
    expect(computeActiveCount({ confidence: [], edgeTypes: [], services: ["svc1"] }, allServices)).toBe(1);
  });
});

describe("effective* helpers", () => {
  it("effectiveAllOn falls back to every name when explicit is empty", () => {
    expect(effectiveAllOn([], ["a", "b"])).toEqual(["a", "b"]);
    expect(effectiveAllOn(["a"], ["a", "b"])).toEqual(["a"]);
  });

  it("effectiveConfidence falls back to DEFAULT_CONFIDENCE when explicit is empty", () => {
    expect(effectiveConfidence([]).sort()).toEqual(["inferred", "static"].sort());
    expect(effectiveConfidence(["unknown"])).toEqual(["unknown"]);
  });
});
