import { describe, it, expect } from "vitest";
import {
  parseConfig,
  serialize,
  toModel,
  setField,
  pushIn,
  removeAt,
  sectionForError,
  countCommentsInSection,
} from "./configYaml";

const FIXTURE = `# top-level header comment
name: fleet
version: "1"
services:
  # the auth service
  - name: auth
    path: ./auth
    language: go
    port: 8080
links:
  - from: auth
    to: users
    via: http
index:
  exclude:
    - "**/node_modules/**" # generated
settings:
  snippet_lines: 30
  default_layout: dagre-lr
  default_depth: 5
  port: 9400
evidence:
  contract_globs:
    - "**/*.proto"
`;

describe("configYaml", () => {
  it("parses valid YAML into a Document with no error", () => {
    const { doc, error } = parseConfig(FIXTURE);
    expect(error).toBeNull();
    expect(doc).not.toBeNull();
  });

  it("returns a parse error for invalid YAML", () => {
    const { doc, error } = parseConfig("services: [\n  - broken");
    expect(doc).toBeNull();
    expect(error).toBeTruthy();
  });

  it("round-trips untouched sections byte-identically when a different section is edited", () => {
    const { doc } = parseConfig(FIXTURE);
    setField(doc!, ["services", 0, "port"], 9090);
    const out = serialize(doc!);

    // The edited line changed...
    expect(out).toContain("port: 9090");
    // ...but every other line, including comments, is untouched.
    const untouchedLines = FIXTURE.split("\n").filter((l) => !l.includes("port: 8080"));
    for (const line of untouchedLines) {
      if (line.trim() === "") continue;
      expect(out).toContain(line);
    }
    expect(out).toContain("# top-level header comment");
    expect(out).toContain("# the auth service");
    expect(out).toContain("# generated");
  });

  it("toModel reflects edits made via setField", () => {
    const { doc } = parseConfig(FIXTURE);
    setField(doc!, ["services", 0, "name"], "auth-renamed");
    const model = toModel(doc!);
    expect(model.services[0].name).toBe("auth-renamed");
  });

  it("pushIn appends a new row and removeAt removes it", () => {
    const { doc } = parseConfig(FIXTURE);
    pushIn(doc!, ["index", "exclude"], "**/vendor/**");
    expect(toModel(doc!).index.exclude).toEqual(["**/node_modules/**", "**/vendor/**"]);

    removeAt(doc!, ["index", "exclude"], 0);
    expect(toModel(doc!).index.exclude).toEqual(["**/vendor/**"]);
  });

  it("setField preserves a scalar's inline comment when only its value changes", () => {
    const { doc } = parseConfig(FIXTURE);
    // yaml's setIn mutates the existing Scalar's value in place rather than
    // replacing the node, so the "# generated" trailing comment survives.
    const result = setField(doc!, ["index", "exclude", 0], "**/dist/**");
    expect(result.lostComment).toBeNull();
    expect(serialize(doc!)).toContain('"**/dist/**" # generated');
  });

  it("setField reports no lost comment for an uncommented field", () => {
    const { doc } = parseConfig(FIXTURE);
    const result = setField(doc!, ["services", 0, "language"], "python");
    expect(result.lostComment).toBeNull();
  });

  it("sectionForError maps a yaml line-numbered error to the enclosing top-level section", () => {
    const section = sectionForError(FIXTURE, "parse workspace config: yaml: line 8: mapping values are not allowed");
    expect(section).toBe("services");
  });

  it("sectionForError falls back to 'services' for service-path errors with no line number", () => {
    const section = sectionForError(FIXTURE, 'service auth: path "./missing" does not exist or is not a directory');
    expect(section).toBe("services");
  });

  it("sectionForError returns null when nothing matches", () => {
    expect(sectionForError(FIXTURE, "totally unrelated error")).toBeNull();
  });

  it("countCommentsInSection counts comments only within a section's line range", () => {
    expect(countCommentsInSection(FIXTURE, "services")).toBe(1);
    expect(countCommentsInSection(FIXTURE, "index")).toBe(1);
    expect(countCommentsInSection(FIXTURE, "settings")).toBe(0);
  });
});
