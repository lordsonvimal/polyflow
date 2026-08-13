import { describe, it, expect } from "vitest";
import { parseQuery, parseNodeCard } from "./query";

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
