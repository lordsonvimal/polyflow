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

export function downloadBlob(filename: string, blob: Blob): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}
