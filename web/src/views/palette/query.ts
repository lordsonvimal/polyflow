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

// The hybrid search response's Entity has no Label/Service field (see
// internal/semantic/embedder.go) — nodeCardText encodes them positionally
// as "label type service file …" (internal/semantic/corpus.go), so a hit
// from the fused path is parsed back out of its card text.
export function parseNodeCard(text: string): { label: string; type: string; service: string } {
  const parts = text.split(" ");
  return { label: parts[0] ?? "", type: parts[1] ?? "", service: parts[2] ?? "" };
}
