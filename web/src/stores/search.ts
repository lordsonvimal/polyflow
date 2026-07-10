import { createSignal, createEffect } from "solid-js";
import { GraphNode } from "./graph";

// ── localStorage: recently searched terms ──────────────────────────────────

const RECENT_KEY = "pf:recent_searches";
const MAX_RECENT = 10;

function loadRecent(): string[] {
  try {
    return JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]");
  } catch { return []; }
}

function saveRecent(term: string) {
  const prev = loadRecent().filter((t) => t !== term);
  const next = [term, ...prev].slice(0, MAX_RECENT);
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(next)); } catch { /* ignore */ }
  setRecentSearches(next);
}

// ── URL: persist ?search= ──────────────────────────────────────────────────

function urlParam(key: string): string | null {
  return new URLSearchParams(window.location.search).get(key);
}

function pushSearchURL(q: string) {
  const sp = new URLSearchParams(window.location.search);
  if (q) sp.set("search", q); else sp.delete("search");
  const query = sp.toString();
  window.history.replaceState(null, "", query ? `?${query}` : window.location.pathname);
}

// ── Signals ────────────────────────────────────────────────────────────────

export interface FileResult {
  file: string;
  service: string;
  counts: Record<string, number>;
}

const initQuery = urlParam("search") ?? "";
const [query, setQueryRaw] = createSignal(initQuery);
const [kind, setKind] = createSignal<string>(""); // "" = all node types
const [results, setResults] = createSignal<GraphNode[]>([]);
const [fileResults, setFileResults] = createSignal<FileResult[]>([]);
const [searching, setSearching] = createSignal(false);
const [recentSearches, setRecentSearches] = createSignal<string[]>(loadRecent());

let debounceTimer: ReturnType<typeof setTimeout> | undefined;

function setQuery(q: string) {
  setQueryRaw(q);
  pushSearchURL(q);
}

createEffect(() => {
  const q = query();
  const k = kind();
  clearTimeout(debounceTimer);
  if (q.trim().length < 2) {
    setResults([]);
    setFileResults([]);
    return;
  }
  debounceTimer = setTimeout(async () => {
    setSearching(true);
    try {
      const kindParam = k ? `&kind=${encodeURIComponent(k)}` : "";
      const [nodeRes, fileRes] = await Promise.all([
        fetch(`/api/graph/search?q=${encodeURIComponent(q)}&limit=20${kindParam}`),
        fetch(`/api/files?q=${encodeURIComponent(q)}&limit=10`),
      ]);
      if (nodeRes.ok) {
        setResults((await nodeRes.json()) as GraphNode[]);
      }
      if (fileRes.ok) {
        const data = await fileRes.json();
        setFileResults((data.files ?? []) as FileResult[]);
      }
    } finally {
      setSearching(false);
    }
  }, 200);
});

export const searchStore = {
  query,
  setQuery,
  kind,
  setKind,
  results,
  fileResults,
  searching,
  recentSearches,
  saveRecent,
};
