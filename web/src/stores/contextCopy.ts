import { createSignal } from "solid-js";
import { ApiError } from "../lib/apiFetch";
import { downloadText } from "../lib/export";
import {
  buildRequest,
  fetchBundle,
  type BundleRequest,
  type BundleResponse,
  type CopyMode,
  type CopySource,
} from "../views/context/copy";
import { drawerStore } from "./drawer";
import { notificationsStore } from "./notifications";
import { scopeStore } from "./scope";

export const TOKEN_BUDGETS = [2000, 8000, 32000] as const;

export interface RecentBundle {
  id: string;
  label: string;
  request: BundleRequest;
  response: BundleResponse;
  at: number;
}

const RECENT_MAX = 10;

const [mode, setMode] = createSignal<CopyMode>("viewed");
const [depth, setDepth] = createSignal(2);
const [snippets, setSnippets] = createSignal(true);
const [maxTokens, setMaxTokens] = createSignal<number>(TOKEN_BUDGETS[1]);
const [loading, setLoading] = createSignal(false);
const [error, setError] = createSignal<string | null>(null);
const [result, setResult] = createSignal<BundleResponse | null>(null);
const [requestNote, setRequestNote] = createSignal<string | undefined>(undefined);
const [rawView, setRawView] = createSignal(false);
const [recent, setRecent] = createSignal<RecentBundle[]>([]);

function parseApiErrorMessage(err: ApiError): string {
  try {
    const body = JSON.parse(err.body) as { error?: string };
    return body.error || err.body || err.message;
  } catch {
    return err.body || err.message;
  }
}

function labelFor(source: CopySource): string {
  switch (source.kind) {
    case "node": return `node ${source.id}`;
    case "edge": return `edge ${source.id}`;
    case "flow": return `flow ${source.id}`;
    case "group": return `${source.ids.length} node${source.ids.length === 1 ? "" : "s"}`;
    case "scope": return "current scope";
  }
}

async function copy(source: CopySource): Promise<void> {
  setError(null);
  setLoading(true);
  drawerStore.openContext();
  const { request, note } = buildRequest(source, {
    mode: mode(),
    depth: depth(),
    snippets: snippets(),
    maxTokens: maxTokens(),
  });
  setRequestNote(note);
  try {
    const resp = await fetchBundle(request);
    setResult(resp);
    setRecent((prev) => [
      { id: `bundle-${Date.now()}`, label: labelFor(source), request, response: resp, at: Date.now() },
      ...prev,
    ].slice(0, RECENT_MAX));
  } catch (err) {
    // UB.6 error bodies are `{"error": "unknown id(s): …"}` (writeError,
    // handlers.go) — surfaced verbatim (just unwrapped from its JSON
    // envelope) rather than re-worded, per the plan's error contract.
    setError(err instanceof ApiError ? parseApiErrorMessage(err) : err instanceof Error ? err.message : String(err));
    setResult(null);
  } finally {
    setLoading(false);
  }
}

function reopen(bundle: RecentBundle): void {
  setResult(bundle.response);
  setError(null);
  setRequestNote(undefined);
  drawerStore.openContext();
}

async function copyToClipboard(): Promise<void> {
  const r = result();
  if (!r) return;
  try {
    await navigator.clipboard?.writeText(r.markdown);
    notificationsStore.add({ id: `copy-context-${Date.now()}`, kind: "success", message: "Context copied to clipboard" });
  } catch {
    notificationsStore.add({ id: `copy-context-err-${Date.now()}`, kind: "error", message: "Could not copy to clipboard" });
  }
}

function downloadMarkdown(): void {
  const r = result();
  if (!r) return;
  downloadText(`polyflow-context-${new Date().toISOString().slice(0, 10)}.md`, r.markdown, "text/markdown");
}

// "unknown id after reindex" per the plan's error contract — the scope's
// ids are stale everywhere, not just for this bundle, so this uses the same
// full-reset path the graph loader already applies for a stale canvas id.
function refreshView(): void {
  setError(null);
  scopeStore.handleStaleId();
}

export const contextCopyStore = {
  mode, setMode,
  depth, setDepth,
  snippets, setSnippets,
  maxTokens, setMaxTokens,
  loading,
  error,
  result,
  requestNote,
  rawView, setRawView,
  recent,
  copy,
  reopen,
  copyToClipboard,
  downloadMarkdown,
  refreshView,
};
