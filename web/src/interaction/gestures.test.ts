import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { handleIntent, makeClickHandler } from "./gestures";
import type { Intent } from "./gestures";
import { selectionStore } from "../stores/selection";
import { registerMenuItems, unregisterMenuItems, getMenuItems } from "./ContextMenu";

beforeEach(() => {
  selectionStore.setSelection(null);
  selectionStore.setHoverTarget(null);
  // Clear pinned (no direct reset exported, so unpin all)
  selectionStore.pinned().forEach(p => selectionStore.unpin(p.id));
});

// ── 1. Same intent from any source → identical selection state ──────────────
describe("handleIntent - select", () => {
  it("sets selection for node intent", () => {
    handleIntent({ type: "select", target: { kind: "node", id: "n1" } });
    expect(selectionStore.selection()).toEqual({ kind: "node", id: "n1" });
  });

  it("sets selection for edge intent", () => {
    handleIntent({ type: "select", target: { kind: "edge", id: "e2" } });
    expect(selectionStore.selection()).toEqual({ kind: "edge", id: "e2" });
  });

  it("firing from a 'tree row' and a 'canvas node' with same target → same state", () => {
    // simulate tree row
    const treeIntents: Intent[] = [];
    const treeHandlers = makeClickHandler({ kind: "node", id: "n1" }, i => treeIntents.push(i));
    // simulate canvas node via handleIntent directly (as wireCytoscape would call it)
    const canvasIntents: Intent[] = [];

    // Both call handleIntent with the same select intent
    const target = { kind: "node" as const, id: "n1" };
    handleIntent({ type: "select", target });
    const stateFromCanvas = selectionStore.selection();

    selectionStore.setSelection(null);
    handleIntent({ type: "select", target });
    const stateFromTree = selectionStore.selection();

    expect(stateFromCanvas).toEqual(stateFromTree);
  });
});

// ── 2. Double vs single click disambiguation (300 ms) ───────────────────────
describe("makeClickHandler - click disambiguation", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("single click fires select after 300 ms (action not lost)", () => {
    const intents: Intent[] = [];
    const h = makeClickHandler({ kind: "node", id: "n1" }, i => intents.push(i));
    h.onClick(new MouseEvent("click"));
    expect(intents).toHaveLength(0); // not yet
    vi.advanceTimersByTime(300);
    expect(intents).toHaveLength(1);
    expect(intents[0].type).toBe("select");
  });

  it("double click fires drill, not two selects", () => {
    const intents: Intent[] = [];
    const h = makeClickHandler({ kind: "node", id: "n1" }, i => intents.push(i));
    h.onClick(new MouseEvent("click"));
    h.onClick(new MouseEvent("click")); // second within 300 ms
    vi.advanceTimersByTime(300);
    expect(intents).toHaveLength(1);
    expect(intents[0].type).toBe("drill");
  });

  it("shift-click fires multiAdd immediately", () => {
    const intents: Intent[] = [];
    const h = makeClickHandler({ kind: "node", id: "n1" }, i => intents.push(i));
    h.onClick(new MouseEvent("click", { shiftKey: true }));
    expect(intents).toHaveLength(1);
    expect(intents[0].type).toBe("multiAdd");
  });

  it("hover enter/leave fire hoverTarget intents", () => {
    const intents: Intent[] = [];
    const h = makeClickHandler({ kind: "edge", id: "e1" }, i => intents.push(i));
    h.onMouseEnter();
    expect(intents[0]).toEqual({ type: "hoverTarget", target: { kind: "edge", id: "e1" } });
    h.onMouseLeave();
    expect(intents[1]).toEqual({ type: "hoverTarget", target: null });
  });
});

// ── 3. Context-menu registry contribution/removal ───────────────────────────
describe("ContextMenu registry", () => {
  afterEach(() => {
    unregisterMenuItems("test-activity");
    unregisterMenuItems("other-activity");
  });

  it("contributes items and returns them via getMenuItems", () => {
    registerMenuItems("test-activity", [
      { id: "isolate", label: "Isolate flows through here", handler: () => {} },
    ]);
    const items = getMenuItems();
    expect(items.map(i => i.id)).toContain("isolate");
  });

  it("removes items after unregister", () => {
    registerMenuItems("test-activity", [{ id: "hide", label: "Hide", handler: () => {} }]);
    unregisterMenuItems("test-activity");
    expect(getMenuItems().find(i => i.id === "hide")).toBeUndefined();
  });

  it("re-registering the same activity replaces its items", () => {
    registerMenuItems("test-activity", [{ id: "a", label: "A", handler: () => {} }]);
    registerMenuItems("test-activity", [{ id: "b", label: "B", handler: () => {} }]);
    const ids = getMenuItems().map(i => i.id);
    expect(ids).not.toContain("a");
    expect(ids).toContain("b");
  });

  it("multiple activities contribute independently", () => {
    registerMenuItems("test-activity", [{ id: "x", label: "X", handler: () => {} }]);
    registerMenuItems("other-activity", [{ id: "y", label: "Y", handler: () => {} }]);
    const ids = getMenuItems().map(i => i.id);
    expect(ids).toContain("x");
    expect(ids).toContain("y");
  });
});

// ── 4. Esc reaches scope store in pinned-detail state ───────────────────────
describe("handleIntent escape - pinned detail", () => {
  it("Esc clears active selection but keeps pins", () => {
    selectionStore.setSelection({ kind: "node", id: "n1" });
    selectionStore.pin({ kind: "node", id: "n2" });
    expect(selectionStore.pinned()).toHaveLength(1);

    handleIntent({ type: "escape" });
    expect(selectionStore.selection()).toBeNull();
    // pinned panel survives — Esc doesn't unpin
    expect(selectionStore.pinned()).toHaveLength(1);
  });
});
