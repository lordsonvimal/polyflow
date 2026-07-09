// Store-level proofs for the Phase 11 UI behaviors: the confidence filter
// defaults to static+inferred, boundary groups start collapsed with a
// per-group toggle, and the view mode defaults to in-depth.

import { describe, it, expect } from "vitest";
import { uiStore } from "./ui";

describe("uiStore defaults", () => {
  it("renders only static+inferred confidence by default", () => {
    expect(uiStore.activeConfidence()).toEqual(["static", "inferred"]);
  });

  it("starts in the in-depth (per-function) view", () => {
    expect(uiStore.viewMode()).toBe("indepth");
  });

  it("starts with every boundary group collapsed", () => {
    expect(uiStore.expandedBoundaries()).toEqual([]);
  });
});

describe("uiStore toggles", () => {
  it("toggleConfidence opts partial in and out", () => {
    uiStore.toggleConfidence("partial");
    expect(uiStore.activeConfidence()).toContain("partial");
    uiStore.toggleConfidence("partial");
    expect(uiStore.activeConfidence()).not.toContain("partial");
  });

  it("toggleBoundary expands and collapses one group", () => {
    uiStore.toggleBoundary("boundary:svc:gin");
    expect(uiStore.expandedBoundaries()).toEqual(["boundary:svc:gin"]);
    uiStore.toggleBoundary("boundary:svc:gin");
    expect(uiStore.expandedBoundaries()).toEqual([]);
  });

  it("collapseAllBoundaries resets every expanded group", () => {
    uiStore.toggleBoundary("a");
    uiStore.toggleBoundary("b");
    uiStore.collapseAllBoundaries();
    expect(uiStore.expandedBoundaries()).toEqual([]);
  });

  it("hides and shows node types", () => {
    uiStore.toggleHiddenType("function");
    expect(uiStore.hiddenTypes()).toContain("function");
    uiStore.toggleHiddenType("function");
    expect(uiStore.hiddenTypes()).not.toContain("function");
  });
});
