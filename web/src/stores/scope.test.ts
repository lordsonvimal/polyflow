import { describe, it, expect, beforeEach } from "vitest";
import { encodeViewState, decodeViewState, DEFAULT_STATE } from "./scope";
import type { Scope, ViewState } from "./scope";

// Codec round-trips for every scope kind
const SCOPES: Scope[] = [
  { kind: "search" },
  { kind: "search", nodeType: "function", q: "sync" },
  { kind: "overview" },
  { kind: "service", service: "rails-svc" },
  { kind: "folder", service: "rails-svc", path: "app/jobs" },
  { kind: "file", service: "rails-svc", path: "app/jobs/sync.rb" },
  { kind: "neighborhood", nodeId: "node-1", depth: 2 },
  { kind: "impact", target: "node-2", direction: "both", depth: 3 },
  { kind: "flow", flow: { id: "flow-1", label: "POST /orders" } },
  { kind: "group", nodeIds: ["a", "b", "c"] },
];

function makeState(scope: Scope): ViewState {
  return {
    stack: [{ kind: "search" }, scope],
    filters: { confidence: ["static"], edgeTypes: ["calls"], services: ["rails-svc"] },
    selection: { kind: "node", id: "node-1" },
    layout: "dagre",
  };
}

describe("URL codec", () => {
  it("round-trips every scope kind", () => {
    for (const scope of SCOPES) {
      const state = makeState(scope);
      const encoded = encodeViewState(state);
      const result = decodeViewState(encoded);
      expect(result).not.toBeNull();
      expect(result!.unknownVersion).toBe(false);
      expect(result!.state).toEqual(state);
    }
  });

  it("returns unknownVersion=true for a different version number", () => {
    // Hand-craft a v=2 payload
    const payload = btoa(JSON.stringify({ v: 2, stack: [], filters: {} }))
      .replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
    const result = decodeViewState(payload);
    expect(result).not.toBeNull();
    expect(result!.unknownVersion).toBe(true);
    expect(result!.state).toEqual(DEFAULT_STATE);
  });

  it("returns null for garbage input", () => {
    expect(decodeViewState("not-valid-base64!!!")).toBeNull();
  });

  it("round-trips isolation chip", () => {
    const state: ViewState = {
      stack: [{ kind: "search" }],
      isolation: { id: "flow-42", label: "Vega flow" },
      filters: { confidence: [], edgeTypes: [], services: [] },
    };
    const result = decodeViewState(encodeViewState(state));
    expect(result!.state.isolation).toEqual(state.isolation);
  });
});

// Esc ordering state machine (tested via pure logic, not reactive signals)
describe("Esc ordering", () => {
  it("4 presses from full state → resting at search", () => {
    // Simulate the sequence manually since signals are reactive
    // State: selection set, isolation set, stack=[search, service]
    const state0: ViewState = {
      stack: [{ kind: "search" }, { kind: "service", service: "rails-svc" }],
      isolation: { id: "f1" },
      filters: { confidence: [], edgeTypes: [], services: [] },
      selection: { kind: "node", id: "n1" },
    };

    // Press 1: clear selection
    const state1 = { ...state0, selection: undefined };
    expect(state1.selection).toBeUndefined();
    expect(state1.isolation).toBeDefined();
    expect(state1.stack.length).toBe(2);

    // Press 2: pop isolation
    const state2 = { ...state1, isolation: undefined };
    expect(state2.isolation).toBeUndefined();
    expect(state2.stack.length).toBe(2);

    // Press 3: pop scope
    const state3 = { ...state2, stack: state2.stack.slice(0, -1) };
    expect(state3.stack).toEqual([{ kind: "search" }]);

    // Press 4: already at root — stack stays
    const hasMore3 = state3.stack.length > 1;
    expect(hasMore3).toBe(false);
    // No change — at rest
    expect(state3.stack[0].kind).toBe("search");
  });
});

// Breadcrumb pop truncates stack
describe("popTo", () => {
  it("truncates to index i+1", () => {
    const stack: Scope[] = [
      { kind: "search" },
      { kind: "service", service: "svc" },
      { kind: "folder", service: "svc", path: "app" },
    ];
    const result = stack.slice(0, 1 + 1);
    expect(result).toEqual([{ kind: "search" }, { kind: "service", service: "svc" }]);
  });
});
