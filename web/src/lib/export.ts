// Diagram export: Mermaid text comes from the server (single source of
// truth, golden-tested); SVG/PNG are rendered client-side from the current
// Cytoscape canvas, so they show exactly what the user sees (filters,
// collapse state, layout).

export type MermaidLevel = "service" | "function";

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

export function exportFilename(kind: "mermaid" | "svg" | "png", level?: MermaidLevel): string {
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
