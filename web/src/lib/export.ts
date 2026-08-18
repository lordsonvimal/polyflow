// Diagram export: Mermaid text comes from the server (single source of
// truth, golden-tested); SVG/PNG are rendered client-side from the current
// Cytoscape canvas, so they show exactly what the user sees (filters,
// collapse state, layout).
import type { Core } from "cytoscape";
import type { Scope } from "../stores/scope";

export type MermaidLevel = "service" | "file" | "structure" | "function";

export interface TraceScope {
  root: string;
  direction: string;
  depth: number;
}

export function mermaidURL(level: MermaidLevel, scope?: TraceScope | null): string {
  const sp = new URLSearchParams({ level });
  if (scope) {
    sp.set("root", scope.root);
    sp.set("direction", scope.direction);
    sp.set("depth", String(scope.depth));
  }
  return `/api/export/mermaid?${sp.toString()}`;
}

export function exportFilename(kind: "mermaid" | "svg" | "png" | "json", level?: MermaidLevel): string {
  const stamp = new Date().toISOString().slice(0, 10);
  if (kind === "mermaid") return `polyflow-${level ?? "function"}-${stamp}.mmd`;
  return `polyflow-graph-${stamp}.${kind}`;
}

export async function fetchMermaid(level: MermaidLevel, scope?: TraceScope | null): Promise<string> {
  const res = await fetch(mermaidURL(level, scope));
  if (!res.ok) throw new Error(`export failed: ${res.status}`);
  return res.text();
}

export function downloadText(filename: string, text: string, mime = "text/plain"): void {
  downloadBlob(filename, new Blob([text], { type: mime }));
}

// Browsers cap canvas dimensions (~16k px per side and a total-area limit);
// beyond them toDataURL silently returns an empty image, which used to ship
// as a 0-byte PNG. Clamp the render scale so the output stays inside a safe
// budget instead.
export const MAX_EXPORT_DIM = 8000;
export const MAX_EXPORT_AREA = 32_000_000; // ~32MP, well under Safari's limit

export function safeExportScale(
  width: number,
  height: number,
  desired = 2,
  maxDim = MAX_EXPORT_DIM,
  maxArea = MAX_EXPORT_AREA,
): number {
  if (width <= 0 || height <= 0) return desired;
  const dimScale = Math.min(maxDim / width, maxDim / height);
  const areaScale = Math.sqrt(maxArea / (width * height));
  return Math.max(0.1, Math.min(desired, dimScale, areaScale));
}

export function downloadBlob(filename: string, blob: Blob): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

// UO.5: Mermaid's "level" is the server's granularity for a diagram; every
// Scope kind maps to the closest one. Only the node-anchored kinds
// (neighborhood/impact) carry a resolvable root for a scoped subgraph —
// everything else exports the whole graph at the matched level, since e.g.
// "service" and "file" scopes are keyed by name/path, not a graph node id
// the trace endpoint can walk from.
export function mermaidLevelForScope(scope: Scope): MermaidLevel {
  switch (scope.kind) {
    case "service":
      return "service";
    case "folder":
    case "file":
      return "file";
    case "neighborhood":
    case "impact":
    case "flow":
    case "group":
    case "search":
      return "function";
    case "overview":
    default:
      return "service";
  }
}

export function mermaidTraceScopeFor(scope: Scope): TraceScope | null {
  switch (scope.kind) {
    case "neighborhood":
      return { root: scope.nodeId, direction: "both", depth: scope.depth };
    case "impact":
      return { root: scope.target, direction: scope.direction, depth: scope.depth };
    default:
      return null;
  }
}

// SVG/PNG/JSON render exactly what's on screen: `cy` is the live instance
// (see stores/canvasRef.ts), so filters/collapse/layout are already baked
// into its element set — no separate serialization path to keep in sync.
export function exportSVG(cy: Core): string {
  // cytoscape-svg (registered in CanvasHost) adds .svg(); no bundled types.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (cy as any).svg({ full: true, scale: 1 });
}

// Throws if the browser's canvas rasterizer silently fails (e.g. safeExportScale
// still leaves an unsupported size on some browsers) — caller falls back to SVG.
export function exportPNGBlob(cy: Core): Blob {
  const bb = cy.elements().boundingBox();
  const scale = safeExportScale(bb.w, bb.h);
  const dataUrl = cy.png({ full: true, scale });
  return dataURLToBlob(dataUrl);
}

export function exportElementsJSON(cy: Core): string {
  return JSON.stringify(cy.elements().jsons(), null, 2);
}

function dataURLToBlob(dataUrl: string): Blob {
  const comma = dataUrl.indexOf(",");
  const header = dataUrl.slice(0, comma);
  const base64 = dataUrl.slice(comma + 1);
  const mimeMatch = /data:(.*);base64/.exec(header);
  const mime = mimeMatch ? mimeMatch[1] : "image/png";
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  if (bytes.length === 0) throw new Error("empty PNG render");
  return new Blob([bytes], { type: mime });
}
