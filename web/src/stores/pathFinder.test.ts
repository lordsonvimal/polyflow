import { describe, it, expect, beforeEach } from "vitest";
import { pathFinderStore } from "./pathFinder";

describe("pathFinderStore", () => {
  beforeEach(() => {
    pathFinderStore.clearStart();
    pathFinderStore.consume();
  });

  it("set/replace/clear state machine", () => {
    expect(pathFinderStore.startNode()).toBeNull();

    pathFinderStore.setStart({ id: "a", label: "A" });
    expect(pathFinderStore.startNode()).toEqual({ id: "a", label: "A" });

    pathFinderStore.setStart({ id: "b", label: "B" });
    expect(pathFinderStore.startNode()).toEqual({ id: "b", label: "B" });

    pathFinderStore.clearStart();
    expect(pathFinderStore.startNode()).toBeNull();
  });

  it("requestPaths/consume one-shot bridge", () => {
    expect(pathFinderStore.requestedTo()).toBeNull();
    pathFinderStore.requestPaths({ id: "target", label: "Target" });
    expect(pathFinderStore.requestedTo()).toEqual({ id: "target", label: "Target" });
    pathFinderStore.consume();
    expect(pathFinderStore.requestedTo()).toBeNull();
  });
});
