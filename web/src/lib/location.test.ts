import { describe, it, expect } from "vitest";
import { formatRange, formatLocation } from "./location";

describe("formatRange", () => {
  it("renders a dash range when start and end differ", () => {
    expect(formatRange(12, 48)).toBe("12–48");
  });

  it("falls back to the single line when end_line is 0 (honest unknown)", () => {
    expect(formatRange(12, 0)).toBe("12");
  });

  it("falls back to the single line when end_line is undefined", () => {
    expect(formatRange(12, undefined)).toBe("12");
  });

  it("collapses to the single line when start equals end", () => {
    expect(formatRange(12, 12)).toBe("12");
  });

  it("returns empty string when line is missing", () => {
    expect(formatRange(undefined, 48)).toBe("");
    expect(formatRange(0, 48)).toBe("");
  });
});

describe("formatLocation", () => {
  it("renders file:start–end", () => {
    expect(formatLocation("a.rb", 12, 48)).toBe("a.rb:12–48");
  });

  it("renders file:line when end_line is 0", () => {
    expect(formatLocation("a.rb", 12, 0)).toBe("a.rb:12");
  });

  it("renders the bare file when line is missing", () => {
    expect(formatLocation("a.rb", undefined, undefined)).toBe("a.rb");
  });

  it("returns empty string when file is missing", () => {
    expect(formatLocation(undefined, 12, 48)).toBe("");
    expect(formatLocation("", 12, 48)).toBe("");
  });
});
