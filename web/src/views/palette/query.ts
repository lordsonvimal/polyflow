// Palette input parsing: "sync kind:function service:rails-svc" → chips +
// free-text query. Chip tokens are stripped from anywhere in the string.
export type Chips = { kind?: string; service?: string };
export type ParsedQuery = { chips: Chips; text: string };

const CHIP_RE = /^(kind|service):(\S+)$/;

export function parseQuery(input: string): ParsedQuery {
  const chips: Chips = {};
  const rest: string[] = [];
  for (const tok of input.trim().split(/\s+/).filter(Boolean)) {
    const m = tok.match(CHIP_RE);
    if (m) {
      if (m[1] === "kind") chips.kind = m[2];
      else chips.service = m[2];
    } else {
      rest.push(tok);
    }
  }
  return { chips, text: rest.join(" ") };
}

// Kind quick-filter chips shown under the palette input: label → node type.
// Clicking one toggles a `kind:<type>` token on the query so "I only want
// endpoints" is one click, not typed syntax the user has to know.
export const KIND_FILTERS: { label: string; kind: string }[] = [
  { label: "endpoints", kind: "http_handler" },
  { label: "functions", kind: "function" },
  { label: "methods", kind: "method" },
  { label: "components", kind: "component" },
  { label: "classes", kind: "class" },
];

// toggleKindChip rebuilds the raw palette string with `kind` set, cleared (if
// it was already the active kind), or replaced. service chip and free text are
// preserved.
export function toggleKindChip(input: string, kind: string): string {
  const { chips, text } = parseQuery(input);
  const next = chips.kind === kind ? undefined : kind;
  const parts: string[] = [];
  if (next) parts.push(`kind:${next}`);
  if (chips.service) parts.push(`service:${chips.service}`);
  if (text) parts.push(text);
  return parts.join(" ");
}

// The hybrid search response's Entity has no Label/Service field (see
// internal/semantic/embedder.go) — nodeCardText encodes them positionally
// as "label type service file …" (internal/semantic/corpus.go), so a hit
// from the fused path is parsed back out of its card text.
export function parseNodeCard(text: string): { label: string; type: string; service: string } {
  const parts = text.split(" ");
  return { label: parts[0] ?? "", type: parts[1] ?? "", service: parts[2] ?? "" };
}
