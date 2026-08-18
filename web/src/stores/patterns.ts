// UO.7: Patterns viewer — GET/POST /api/patterns, wrapping the same
// `patterns list`/`patterns add` internals the CLI uses.
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";
import { notificationsStore } from "../stores/notifications";
import { jobsStore } from "./jobs";

export interface PatternInfo {
  name: string;
  language: string;
  node_type?: string;
  edge_type?: string;
  roles?: string[];
  package?: string;
  version_range?: string;
  source: string;
  custom: boolean;
  grammars?: string[];
}

const [patterns, setPatterns] = createSignal<PatternInfo[]>([]);
const [loading, setLoading] = createSignal(false);
const [adding, setAdding] = createSignal(false);
const [addError, setAddError] = createSignal<string | null>(null);

function parseErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    try {
      const body = JSON.parse(err.body) as { error?: string };
      return body.error || err.body || err.message;
    } catch {
      return err.body || err.message;
    }
  }
  return err instanceof Error ? err.message : String(err);
}

async function load(language?: string): Promise<void> {
  setLoading(true);
  try {
    const qs = language ? `?language=${encodeURIComponent(language)}` : "";
    const data = await apiFetchJSON<{ patterns: PatternInfo[] }>(`/api/patterns${qs}`, { silent: true });
    setPatterns(data.patterns ?? []);
  } catch (err) {
    notificationsStore.add({
      id: `patterns-load-err-${Date.now()}`,
      kind: "error",
      message: "Failed to load patterns",
      detail: parseErrorMessage(err),
    });
  } finally {
    setLoading(false);
  }
}

// Returns true on success so the caller can clear its upload form.
async function add(name: string, content: string): Promise<boolean> {
  setAdding(true);
  setAddError(null);
  try {
    await apiFetch("/api/patterns", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, content }),
      silent: true,
    });
    await load();
    notificationsStore.add({
      id: `pattern-add-${Date.now()}`,
      kind: "success",
      message: "Pattern added",
      action: { label: "Re-index now?", onClick: () => jobsStore.startIndex(false) },
    });
    return true;
  } catch (err) {
    if (err instanceof ApiError && err.status === 422) {
      setAddError(parseErrorMessage(err));
      return false;
    }
    notificationsStore.add({
      id: `pattern-add-err-${Date.now()}`,
      kind: "error",
      message: "Failed to add pattern",
      detail: parseErrorMessage(err),
    });
    return false;
  } finally {
    setAdding(false);
  }
}

function clearAddError(): void {
  setAddError(null);
}

export const patternsStore = {
  patterns,
  loading,
  adding,
  addError,
  load,
  add,
  clearAddError,
  reset: () => {
    setPatterns([]);
    setLoading(false);
    setAdding(false);
    setAddError(null);
  },
};
