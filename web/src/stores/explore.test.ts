import { describe, it, expect, beforeEach } from "vitest";
import { exploreStore } from "./explore";

describe("exploreStore", () => {
  beforeEach(() => exploreStore.reset());

  it("defaults to the tree tab with no focused service", () => {
    expect(exploreStore.tab()).toBe("tree");
    expect(exploreStore.focusService()).toBeUndefined();
  });

  it("openStackFor switches to the stack tab and sets the focused service", () => {
    exploreStore.openStackFor("railssvc");
    expect(exploreStore.tab()).toBe("stack");
    expect(exploreStore.focusService()).toBe("railssvc");
  });

  it("clearFocusService releases the focus without changing tabs", () => {
    exploreStore.openStackFor("railssvc");
    exploreStore.clearFocusService();
    expect(exploreStore.tab()).toBe("stack");
    expect(exploreStore.focusService()).toBeUndefined();
  });

  it("setTab switches tabs directly", () => {
    exploreStore.setTab("stack");
    expect(exploreStore.tab()).toBe("stack");
    exploreStore.setTab("tree");
    expect(exploreStore.tab()).toBe("tree");
  });
});
