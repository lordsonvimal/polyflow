// Edge-confidence filtering. The default view renders only static+inferred
// edges — partial/unknown links are opt-in and drawn dashed/dimmed, so the
// graph never silently presents an uncertain connection as fact.

import { GraphEdge } from "./types";

export const CONFIDENCE_LEVELS = ["static", "inferred", "partial", "unknown"] as const;
export type Confidence = (typeof CONFIDENCE_LEVELS)[number];

export const DEFAULT_CONFIDENCE: Confidence[] = ["static", "inferred"];

// Edges without an explicit confidence come from structural AST matches
// (calls/renders within a file) — they are static by construction.
export function edgeConfidence(e: Pick<GraphEdge, "confidence">): Confidence {
  const c = e.confidence ?? "";
  if (c === "inferred" || c === "partial" || c === "unknown") return c;
  return "static";
}

export function isUncertain(e: Pick<GraphEdge, "confidence">): boolean {
  const c = edgeConfidence(e);
  return c === "partial" || c === "unknown";
}

export function filterEdgesByConfidence(
  edges: GraphEdge[],
  active: readonly string[]
): GraphEdge[] {
  return edges.filter((e) => active.includes(edgeConfidence(e)));
}
