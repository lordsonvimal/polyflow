import { describe, it, expect } from "vitest";
import { computeWindow } from "./virtualize";

describe("computeWindow", () => {
  it("windows a large list down to viewport + overscan", () => {
    const w = computeWindow(0, 100, 20, 2792, 2);
    // viewport fits 5 rows, plus 2 overscan on each side (clamped at 0)
    expect(w.start).toBe(0);
    expect(w.end).toBe(7);
    expect(w.topPad).toBe(0);
    expect(w.bottomPad).toBe((2792 - 7) * 20);
  });

  it("scrolls the window forward and keeps it bounded by total", () => {
    const w = computeWindow(2000, 100, 20, 2792, 2);
    // first visible row = 2000/20 = 100
    expect(w.start).toBe(98);
    expect(w.end).toBe(107);
  });

  it("clamps end at total near the bottom of the list", () => {
    const w = computeWindow(2791 * 20, 100, 20, 2792, 2);
    expect(w.end).toBe(2792);
  });

  it("returns an empty window for an empty list", () => {
    const w = computeWindow(0, 100, 20, 0);
    expect(w).toEqual({ start: 0, end: 0, topPad: 0, bottomPad: 0 });
  });
});
