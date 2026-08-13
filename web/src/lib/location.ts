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
