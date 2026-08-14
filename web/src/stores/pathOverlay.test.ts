import { describe, it, expect, beforeEach } from "vitest";
import { pathOverlayStore, OVERLAY_MORE_INDEX } from "./pathOverlay";

describe("pathOverlayStore", () => {
  beforeEach(() => pathOverlayStore.clear());

  it("assigns a distinct color index per path, up to the color count", () => {
    pathOverlayStore.set([
      ["a1", "a2"],
      ["b1"],
      ["c1"],
      ["d1"],
      ["e1"],
    ]);
    const m = pathOverlayStore.assignment();
    expect(m.get("a1")).toBe(0);
    expect(m.get("a2")).toBe(0);
    expect(m.get("b1")).toBe(1);
    expect(m.get("c1")).toBe(2);
    expect(m.get("d1")).toBe(3);
    expect(m.get("e1")).toBe(4);
  });

  it("groups every path past the 5th into the shared 'more' index", () => {
    pathOverlayStore.set([["p0"], ["p1"], ["p2"], ["p3"], ["p4"], ["p5"], ["p6"]]);
    const m = pathOverlayStore.assignment();
    expect(m.get("p4")).toBe(4);
    expect(m.get("p5")).toBe(OVERLAY_MORE_INDEX);
    expect(m.get("p6")).toBe(OVERLAY_MORE_INDEX);
  });

  it("a node shared by several paths keeps its first (best-ranked) path's color", () => {
    pathOverlayStore.set([["shared", "a"], ["shared", "b"]]);
    expect(pathOverlayStore.assignment().get("shared")).toBe(0);
  });

  it("clear empties the assignment", () => {
    pathOverlayStore.set([["a"]]);
    pathOverlayStore.clear();
    expect(pathOverlayStore.assignment().size).toBe(0);
  });
});
