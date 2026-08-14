import { describe, it, expect } from "vitest";
import { parseMarkdownLite } from "./markdownLite";

describe("parseMarkdownLite", () => {
  it("recognizes headings by level", () => {
    const blocks = parseMarkdownLite("# Title\n\n## Section\n\n### Sub\n");
    expect(blocks).toEqual([
      { type: "heading", level: 1, text: "Title" },
      { type: "heading", level: 2, text: "Section" },
      { type: "heading", level: 3, text: "Sub" },
    ]);
  });

  it("extracts a fenced code block with its language", () => {
    const blocks = parseMarkdownLite("```go\nfunc f() {}\n```\n");
    expect(blocks).toEqual([{ type: "code", lang: "go", text: "func f() {}" }]);
  });

  it("groups consecutive bullet lines into one list block", () => {
    const blocks = parseMarkdownLite("- a\n- b\n- c\n");
    expect(blocks).toEqual([{ type: "list", items: ["a", "b", "c"] }]);
  });

  it("reads a blockquote line", () => {
    const blocks = parseMarkdownLite("> Truncated at 8000 tokens: omitted x, y\n");
    expect(blocks).toEqual([{ type: "quote", text: "Truncated at 8000 tokens: omitted x, y" }]);
  });

  it("collapses consecutive plain lines into one paragraph", () => {
    const blocks = parseMarkdownLite("role: target\nfile: a.go:1–10\n");
    expect(blocks).toEqual([{ type: "paragraph", text: "role: target\nfile: a.go:1–10" }]);
  });

  it("round-trips a full bundle-shaped document without losing sections", () => {
    const md = [
      "# Context: foo",
      "",
      "## svc",
      "",
      "### app/a.go (go)",
      "",
      "**foo** `app/a.go:1–5`",
      "role: target",
      "```go",
      "func foo() {}",
      "```",
      "",
      "## Flow",
      "",
      "- A —calls→ B",
      "",
      "_polyflow context bundle, 1 nodes, ~10 tokens_",
    ].join("\n");
    const blocks = parseMarkdownLite(md);
    expect(blocks.filter((b) => b.type === "heading")).toHaveLength(4);
    expect(blocks.find((b) => b.type === "code")).toEqual({ type: "code", lang: "go", text: "func foo() {}" });
    expect(blocks.find((b) => b.type === "list")).toEqual({ type: "list", items: ["A —calls→ B"] });
  });
});
