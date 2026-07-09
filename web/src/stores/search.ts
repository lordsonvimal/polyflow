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

const initQuery = urlParam("search") ?? "";
const [query, setQueryRaw] = createSignal(initQuery);
const [results, setResults] = createSignal<GraphNode[]>([]);
const [searching, setSearching] = createSignal(false);
const [recentSearches, setRecentSearches] = createSignal<string[]>(loadRecent());

let debounceTimer: ReturnType<typeof setTimeout> | undefined;

function setQuery(q: string) {
  setQueryRaw(q);
  pushSearchURL(q);
}

createEffect(() => {
  const q = query();
  clearTimeout(debounceTimer);
  if (q.trim().length < 2) {
    setResults([]);
    return;
  }
  debounceTimer = setTimeout(async () => {
    setSearching(true);
    try {
      const res = await fetch(`/api/graph/search?q=${encodeURIComponent(q)}&limit=20`);
      if (!res.ok) return;
      const data: GraphNode[] = await res.json();
      setResults(data);
    } finally {
      setSearching(false);
    }
  }, 200);
});

export const searchStore = {
  query,
  setQuery,
  results,
  searching,
  recentSearches,
  saveRecent,
};
