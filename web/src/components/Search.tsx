import { Component, Show, For } from "solid-js";
import { searchStore } from "../stores/search";
import { uiStore } from "../stores/ui";
import { graphStore } from "../stores/graph";
import { fileGroupId } from "../lib/filegroup";

// Search → pick a result → that node becomes the trace root and the graph
// isolates to its subgraph (direction/depth adjustable in the trace panel).
const Search: Component = () => {
  function selectResult(nodeId: string) {
    uiStore.setSelectedNodeId(nodeId);
    searchStore.saveRecent(searchStore.query());
    graphStore.fetchTrace(nodeId, "both", graphStore.traceDepth());
    searchStore.setQuery("");
  }

  // Picking a file selects its group container (grouping is switched on if
  // off, so the selection is actually visible).
  function selectFile(service: string, file: string) {
    searchStore.saveRecent(searchStore.query());
    uiStore.setGroupByFile(true);
    uiStore.setSelectedNodeId(fileGroupId(service, file));
    searchStore.setQuery("");
  }

  return (
    <div class="flex flex-col gap-2">
      <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Search</label>
      <input
        type="search"
        placeholder="function, route, file…"
        value={searchStore.query()}
        onInput={(e) => searchStore.setQuery(e.currentTarget.value)}
        class="w-full rounded bg-gray-800 border border-gray-700 px-3 py-2 text-sm text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
      />
      {/* Kind filter chips */}
      <div class="flex flex-wrap gap-1">
        <For each={[["", "all"], ["function", "fn"], ["variable", "var"], ["struct", "struct"], ["class", "class"], ["http_handler", "handler"]] as [string, string][]}>
          {([value, label]) => (
            <button
              class={`rounded px-1.5 py-0.5 text-[10px] cursor-pointer ${
                searchStore.kind() === value
                  ? "bg-indigo-600 text-white"
                  : "bg-gray-800/70 hover:bg-gray-700 text-gray-400"
              }`}
              onClick={() => searchStore.setKind(value)}
            >
              {label}
            </button>
          )}
        </For>
      </div>
      <Show when={searchStore.searching()}>
        <span class="text-[10px] text-gray-500">searching…</span>
      </Show>
      <Show when={searchStore.fileResults().length > 0}>
        <div>
          <span class="text-[10px] uppercase tracking-wide text-gray-500">Files</span>
          <ul class="flex flex-col gap-1 max-h-32 overflow-y-auto">
            <For each={searchStore.fileResults()}>
              {(f) => (
                <li>
                  <button
                    class="w-full text-left text-xs text-gray-300 hover:text-indigo-300 px-2 py-1 rounded hover:bg-gray-800 cursor-pointer truncate"
                    title={f.file}
                    onClick={() => selectFile(f.service, f.file)}
                  >
                    <span class="font-medium">{f.file}</span>
                    <span class="text-gray-600 ml-1">{f.service}</span>
                  </button>
                </li>
              )}
            </For>
          </ul>
        </div>
      </Show>
      <Show when={searchStore.results().length > 0}>
        <ul class="flex flex-col gap-1 max-h-56 overflow-y-auto">
          <For each={searchStore.results()}>
            {(node) => (
              <li>
                <button
                  class="w-full text-left text-xs text-gray-300 hover:text-indigo-300 px-2 py-1 rounded hover:bg-gray-800 cursor-pointer"
                  title={`${node.file}:${node.line}`}
                  onClick={() => selectResult(node.id)}
                >
                  <span class="font-medium">{node.label}</span>
                  <span class="text-gray-500 ml-1">{node.type}</span>
                  <span class="text-gray-600 ml-1">{node.service}</span>
                </button>
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={!searchStore.query() && searchStore.recentSearches().length > 0}>
        <div class="flex flex-wrap gap-1">
          <For each={searchStore.recentSearches().slice(0, 5)}>
            {(term) => (
              <button
                class="rounded bg-gray-800/70 hover:bg-gray-700 text-[10px] text-gray-400 px-1.5 py-0.5 cursor-pointer"
                onClick={() => searchStore.setQuery(term)}
              >
                {term}
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
};

export default Search;
