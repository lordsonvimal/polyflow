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

  it("hides variables by default", () => {
    expect(uiStore.showVariables()).toBe(false);
  });

  it("groups by file by default (fresh landing)", () => {
    expect(uiStore.groupByFile()).toBe(true);
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

  it("setGroupByFile toggles without persisting to localStorage (Phase U.2)", () => {
    uiStore.setGroupByFile(false);
    expect(uiStore.groupByFile()).toBe(false);
    expect(localStorage.getItem("pf:groupByFile")).toBeNull();
    uiStore.setGroupByFile(true);
    expect(uiStore.groupByFile()).toBe(true);
    expect(localStorage.getItem("pf:groupByFile")).toBeNull();
  });

  it("setShowVariables opts variables in and out (persisted)", () => {
    uiStore.setShowVariables(true);
    expect(uiStore.showVariables()).toBe(true);
    expect(localStorage.getItem("pf:showVariables")).toBe("on");
    uiStore.setShowVariables(false);
    expect(uiStore.showVariables()).toBe(false);
    expect(localStorage.getItem("pf:showVariables")).toBe("off");
  });
});
