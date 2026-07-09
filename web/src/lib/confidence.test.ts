import { describe, it, expect } from "vitest";
import {
  DEFAULT_CONFIDENCE,
  edgeConfidence,
  filterEdgesByConfidence,
  isUncertain,
} from "./confidence";
import { GraphEdge } from "./types";

const mk = (id: string, confidence?: string): GraphEdge => ({
  id,
  from: "a",
  to: "b",
  type: "calls",
  confidence,
});

describe("confidence defaults", () => {
  it("defaults to static + inferred only — partial/unknown are opt-in", () => {
    expect(DEFAULT_CONFIDENCE).toEqual(["static", "inferred"]);
  });

  it("treats edges without a confidence value as static", () => {
    expect(edgeConfidence(mk("e"))).toBe("static");
    expect(edgeConfidence(mk("e", ""))).toBe("static");
  });
});

describe("filterEdgesByConfidence", () => {
  const all = [mk("s", "static"), mk("i", "inferred"), mk("p", "partial"), mk("u", "unknown"), mk("none")];

  it("drops partial and unknown edges under the default view", () => {
    const visible = filterEdgesByConfidence(all, DEFAULT_CONFIDENCE);
    expect(visible.map((e) => e.id)).toEqual(["s", "i", "none"]);
  });

  it("includes uncertain edges once opted in", () => {
    const visible = filterEdgesByConfidence(all, ["static", "inferred", "partial", "unknown"]);
    expect(visible).toHaveLength(5);
  });
});

describe("isUncertain", () => {
  it("flags exactly partial and unknown", () => {
    expect(isUncertain(mk("e", "partial"))).toBe(true);
    expect(isUncertain(mk("e", "unknown"))).toBe(true);
    expect(isUncertain(mk("e", "static"))).toBe(false);
    expect(isUncertain(mk("e"))).toBe(false);
  });
});
