import { createSignal, createEffect } from "solid-js";
import { graphStore } from "./graph";

const [query, setQuery] = createSignal("");
const [debouncedQuery, setDebouncedQuery] = createSignal("");

// Debounce search by 300 ms
createEffect(() => {
  const q = query();
  const timer = setTimeout(() => setDebouncedQuery(q), 300);
  return () => clearTimeout(timer);
});

// Trigger graph fetch when debounced query changes
createEffect(() => {
  const q = debouncedQuery();
  if (q.trim().length >= 2) {
    graphStore.fetchGraph(q);
  }
});

export const searchStore = { query, setQuery, debouncedQuery };
