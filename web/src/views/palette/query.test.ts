import { describe, it, expect } from "vitest";
import { parseQuery, parseNodeCard, toggleKindChip } from "./query";

describe("parseQuery", () => {
  it("splits chip tokens from free text, in any order", () => {
    expect(parseQuery("sync kind:function service:rails-svc")).toEqual({
      chips: { kind: "function", service: "rails-svc" },
      text: "sync",
    });
    expect(parseQuery("kind:route sync orders")).toEqual({
      chips: { kind: "route" },
      text: "sync orders",
    });
  });

  it("returns empty text/chips for blank input", () => {
    expect(parseQuery("")).toEqual({ chips: {}, text: "" });
    expect(parseQuery("   ")).toEqual({ chips: {}, text: "" });
  });

  it("handles chip-only input with no free text", () => {
    expect(parseQuery("kind:route service:rails-svc")).toEqual({
      chips: { kind: "route", service: "rails-svc" },
      text: "",
    });
  });
});

describe("toggleKindChip", () => {
  it("adds a kind chip, preserving free text and service", () => {
    expect(toggleKindChip("do-build service:juniper", "http_handler"))
      .toBe("kind:http_handler service:juniper do-build");
  });

  it("clears the chip when the same kind is toggled again", () => {
    expect(toggleKindChip("kind:http_handler do-build", "http_handler")).toBe("do-build");
  });

  it("replaces a different active kind", () => {
    expect(toggleKindChip("kind:function do-build", "http_handler"))
      .toBe("kind:http_handler do-build");
  });
});

describe("parseNodeCard", () => {
  it("reads label/type/service positionally from the corpus card text", () => {
    expect(parseNodeCard("SyncJob.perform function rails-svc jobs/sync.rb")).toEqual({
      label: "SyncJob.perform",
      type: "function",
      service: "rails-svc",
    });
  });

  it("degrades gracefully on empty text", () => {
    expect(parseNodeCard("")).toEqual({ label: "", type: "", service: "" });
  });
});
