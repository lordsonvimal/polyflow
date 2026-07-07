import { createSignal, createEffect } from "solid-js";
import { GraphNode } from "./graph";

const [query, setQuery] = createSignal("");
const [results, setResults] = createSignal<GraphNode[]>([]);
const [searching, setSearching] = createSignal(false);

let debounceTimer: ReturnType<typeof setTimeout> | undefined;

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

export const searchStore = { query, setQuery, results, searching };
