import { describe, it, expect } from "vitest";
import {
  isBoundaryNode,
  boundaryGroupKey,
  applyBoundaryCollapse,
  groupLabel,
  BOUNDARY_GROUP_TYPE,
} from "./boundary";
import { GraphNode, GraphEdge } from "./types";

function node(id: string, over: Partial<GraphNode> = {}): GraphNode {
  return { id, type: "function", label: id, service: "svc", file: "f.go", line: 1, language: "go", ...over };
}

const app = node("app:handler", { label: "CreateUser" });
const s3a = node("app:s3:put", {
  type: "external_service",
  label: "PutObject",
  meta: { package: "github.com/aws/aws-sdk-go", resolved_version: "1.55.8" },
});
const s3b = node("app:s3:get", {
  type: "external_service",
  label: "GetObject",
  meta: { package: "github.com/aws/aws-sdk-go", resolved_version: "1.55.8" },
});
const edges: GraphEdge[] = [
  { id: "e1", from: app.id, to: s3a.id, type: "cloud_call" },
  { id: "e2", from: app.id, to: s3b.id, type: "cloud_call" },
  { id: "e3", from: s3a.id, to: s3b.id, type: "calls" },
];

describe("isBoundaryNode", () => {
  it("treats version-gated matches and external services as boundaries", () => {
    expect(isBoundaryNode(app)).toBe(false);
    expect(isBoundaryNode(s3a)).toBe(true);
    expect(isBoundaryNode(node("n", { meta: { package: "gin" } }))).toBe(true);
  });
});

describe("applyBoundaryCollapse (collapsed by default)", () => {
  it("folds same-package call sites into one group node", () => {
    const r = applyBoundaryCollapse([app, s3a, s3b], edges, []);
    const groupId = boundaryGroupKey(s3a);
    const ids = r.nodes.map((n) => n.id);

    expect(ids).toContain(app.id);
    expect(ids).toContain(groupId);
    expect(ids).not.toContain(s3a.id);
    expect(ids).not.toContain(s3b.id);

    const group = r.nodes.find((n) => n.id === groupId)!;
    expect(group.type).toBe(BOUNDARY_GROUP_TYPE);
    expect(group.label).toContain("@1.55.8");
    expect(group.label).toContain("(2)");
    expect(group.meta?.package).toBe("github.com/aws/aws-sdk-go");
  });

  it("re-routes and dedupes edges; drops intra-group edges", () => {
    const r = applyBoundaryCollapse([app, s3a, s3b], edges, []);
    const groupId = boundaryGroupKey(s3a);
    // e1+e2 collapse into one app→group edge; e3 (inside group) disappears.
    expect(r.edges).toHaveLength(1);
    expect(r.edges[0].from).toBe(app.id);
    expect(r.edges[0].to).toBe(groupId);
    expect(r.edges[0].type).toBe("cloud_call");
  });

  it("expand toggle restores the individual call sites", () => {
    const groupId = boundaryGroupKey(s3a);
    const r = applyBoundaryCollapse([app, s3a, s3b], edges, [groupId]);
    const ids = r.nodes.map((n) => n.id);
    expect(ids).toContain(s3a.id);
    expect(ids).toContain(s3b.id);
    expect(ids).not.toContain(groupId);
    expect(r.edges).toHaveLength(3); // originals untouched
  });

  it("groups per service — same package in another service stays separate", () => {
    const other = node("agent:s3:put", {
      service: "agent",
      type: "external_service",
      meta: { package: "github.com/aws/aws-sdk-go" },
    });
    const r = applyBoundaryCollapse([s3a, other], [], []);
    expect(r.groups).toHaveLength(2);
  });
});

describe("groupLabel", () => {
  it("uses the short package name plus version and count", () => {
    const g = { id: "x", service: "svc", package: "github.com/aws/aws-sdk-go", version: "1.55.8", members: [s3a, s3b] };
    expect(groupLabel(g)).toBe("aws-sdk-go@1.55.8 (2)");
  });
});
