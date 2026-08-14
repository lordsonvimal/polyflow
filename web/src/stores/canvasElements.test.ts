import { describe, it, expect, beforeEach } from "vitest";
import { canvasElementsStore } from "./canvasElements";

describe("canvasElementsStore", () => {
  beforeEach(() => {
    canvasElementsStore.setIds(new Set());
    canvasElementsStore.setClusters(new Map());
  });

  it("has reflects the published id set", () => {
    canvasElementsStore.setIds(new Set(["a", "b"]));
    expect(canvasElementsStore.has("a")).toBe(true);
    expect(canvasElementsStore.has("z")).toBe(false);
  });

  it("expand passes through ids with no cluster", () => {
    const { ids, clusterCount } = canvasElementsStore.expand(["a", "b"]);
    expect(ids.sort()).toEqual(["a", "b"]);
    expect(clusterCount).toBe(0);
  });

  it("expand swaps a collapsed filegrp id for its real member ids", () => {
    canvasElementsStore.setClusters(new Map([["filegrp:svc:a.go", ["n1", "n2"]]]));
    const { ids, clusterCount } = canvasElementsStore.expand(["filegrp:svc:a.go", "other"]);
    expect(ids.sort()).toEqual(["n1", "n2", "other"]);
    expect(clusterCount).toBe(1);
  });

  it("expand counts multiple distinct clusters and dedupes overlapping members", () => {
    canvasElementsStore.setClusters(
      new Map([
        ["filegrp:svc:a.go", ["n1", "n2"]],
        ["filegrp:svc:b.go", ["n2", "n3"]],
      ]),
    );
    const { ids, clusterCount } = canvasElementsStore.expand(["filegrp:svc:a.go", "filegrp:svc:b.go"]);
    expect(ids.sort()).toEqual(["n1", "n2", "n3"]);
    expect(clusterCount).toBe(2);
  });
});
