// UO.2: polyflow.yml round-trips through a `yaml` Document (not a plain
// object) so form-mode edits mutate the live AST in place and comments in
// untouched regions survive `doc.toString()` — a struct round-trip
// (js-yaml `dump(load(x))`, or the server's own `workspace.Save`) always
// drops them.
import { parseDocument, Document, isSeq, YAMLSeq } from "yaml";

export interface ParseResult {
  doc: Document | null;
  error: string | null;
}

export function parseConfig(raw: string): ParseResult {
  try {
    const doc = parseDocument(raw, { merge: false });
    if (doc.errors.length > 0) {
      return { doc: null, error: doc.errors[0].message };
    }
    return { doc, error: null };
  } catch (err) {
    return { doc: null, error: err instanceof Error ? err.message : String(err) };
  }
}

export function serialize(doc: Document): string {
  return doc.toString();
}

// Plain-JS snapshot of the doc for form rendering (keys are the yaml tags,
// e.g. "services", "index" -> "exclude", "evidence" -> "contract_globs").
// Never mutate this directly — go through the setField/pushIn/removeAt
// helpers below so comments in the underlying AST survive.
export function toModel(doc: Document): Record<string, any> {
  return (doc.toJS() ?? {}) as Record<string, any>;
}

function ensureSeqIn(doc: Document, path: (string | number)[]): YAMLSeq {
  let node = doc.getIn(path, true);
  if (!isSeq(node)) {
    node = doc.createNode([]);
    doc.setIn(path, node);
  }
  return node as YAMLSeq;
}

export interface FieldEditResult {
  // Non-null when the field being overwritten carried a comment that the
  // new value's node does not — surfaced by the caller before save rather
  // than dropped silently (trust contract: no silent gaps).
  lostComment: string | null;
}

function nodeComment(node: unknown): string | null {
  if (!node || typeof node !== "object") return null;
  const n = node as { comment?: string | null; commentBefore?: string | null };
  return n.comment || n.commentBefore || null;
}

export function setField(doc: Document, path: (string | number)[], value: unknown): FieldEditResult {
  const oldComment = nodeComment(doc.getIn(path, true));
  doc.setIn(path, value);
  const newComment = nodeComment(doc.getIn(path, true));
  return { lostComment: oldComment && !newComment ? oldComment : null };
}

// Appends `value` to the seq at `path` (creating the seq if absent).
export function pushIn(doc: Document, path: (string | number)[], value: unknown): void {
  const seq = ensureSeqIn(doc, path);
  seq.items.push(doc.createNode(value));
}

// Removes index `index` from the seq at `path`; returns any comment
// attached to the removed item (lost by definition — there's no "new node"
// to carry it, so the caller should warn).
export function removeAt(doc: Document, path: (string | number)[], index: number): string | null {
  const seq = doc.getIn(path, true);
  if (!isSeq(seq) || index < 0 || index >= seq.items.length) return null;
  const [removed] = seq.items.splice(index, 1);
  return nodeComment(removed);
}

// Sets (adds/overwrites/removes-when-value-is-undefined) a key in the map
// at `path` — used for evidence.runtime.service_names (otel name -> service).
export function setMapEntry(doc: Document, path: (string | number)[], key: string, value: string | undefined): void {
  if (value === undefined) {
    doc.deleteIn([...path, key]);
    return;
  }
  doc.setIn([...path, key], value);
}

const TOP_LEVEL_KEY_RE = /^([a-zA-Z_][\w-]*):/;

// Best-effort map from a 422 `workspace.Load` error string to the Form-mode
// section that caused it, so the editor can point the user at the right
// tab instead of just showing the raw server string. The server error is
// plain text (no structured line/section payload — see config.go's
// writeError(w, 422, err.Error())), so this is pattern matching, not exact:
// - yaml parse/unmarshal errors carry "line N" -> map N back to the
//   nearest preceding top-level key in the raw text.
// - service-path/duplicate-root errors name "service <name>" but no line.
export function sectionForError(raw: string, message: string): string | null {
  const lineMatch = message.match(/line (\d+)/);
  if (lineMatch) {
    const lineNo = parseInt(lineMatch[1], 10);
    const lines = raw.split("\n");
    let section: string | null = null;
    for (let i = 0; i < Math.min(lineNo, lines.length); i++) {
      const m = lines[i].match(TOP_LEVEL_KEY_RE);
      if (m) section = m[1];
    }
    return section;
  }
  if (/\bservice[s]? /.test(message)) return "services";
  if (/\blink[s]? /.test(message)) return "links";
  return null;
}

// Counts "# ..." comment markers within the raw-text line range owned by a
// top-level section (from its own "key:" line up to the next top-level
// key). Used pre-save as a cheap, honest "you're about to drop N comments
// in this section" check for edits the AST-mutation helpers above can't
// fully protect (e.g. replacing a whole seq item wholesale).
export function countCommentsInSection(raw: string, section: string): number {
  const lines = raw.split("\n");
  let inSection = false;
  let count = 0;
  for (const line of lines) {
    const m = line.match(TOP_LEVEL_KEY_RE);
    if (m) {
      inSection = m[1] === section;
      if (inSection && line.includes("#")) count++;
      continue;
    }
    if (inSection && /^\s*#|\s#/.test(line) && line.includes("#")) count++;
  }
  return count;
}
