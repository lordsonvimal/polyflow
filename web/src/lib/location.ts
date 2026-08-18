// Canonical `file:12–48` location formatting (UN.3) — the single place every
// view (tree chips, palette rows, tooltips, detail/source panel) formats a
// node's line range, so the "never fabricate an end" rule (end_line 0 →
// `:12`, not `:12–0`) is enforced once instead of per call site.

export function formatRange(line?: number, endLine?: number): string {
  if (!line) return "";
  return endLine && endLine !== 0 && endLine !== line ? `${line}–${endLine}` : `${line}`;
}

export function formatLocation(file?: string, line?: number, endLine?: number): string {
  if (!file) return "";
  const range = formatRange(line, endLine);
  return range ? `${file}:${range}` : file;
}

// A label is only ever a full filesystem path when nothing more specific
// (a symbol, service, or node name) was available upstream — in that case
// the core display label (canvas node text, breadcrumb crumb) should show
// just the file name; the full path stays available via title/tooltip.
export function displayLabel(label: string): string {
  // Route labels ("POST /orders") and other multi-word strings also contain
  // "/" but aren't paths — only a single space-free token is a candidate.
  if (label.includes(" ") || !label.includes("/")) return label;
  const parts = label.split("/").filter(Boolean);
  return parts.at(-1) ?? label;
}

// dom_target nodes (addEventListener/removeEventListener call sites) all
// share the bare method name as their label — a file with an add/remove
// pair, or multiple listeners on different events, renders as visually
// identical circles with no way to tell which is which without opening the
// detail panel. Every dom_target carries meta.event (the listener's event
// string), so surface it in the label instead.
export function nodeDisplayLabel(n: { label: string; type?: string; meta?: Record<string, string> }): string {
  const base = displayLabel(n.label);
  if (n.type === "dom_target" && n.meta?.event) return `${base} (${n.meta.event})`;
  return base;
}
