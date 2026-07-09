import { Component, Show, For } from "solid-js";
import { searchStore } from "../stores/search";
import { uiStore } from "../stores/ui";
import { graphStore } from "../stores/graph";

const Search: Component = () => {
  return (
    <div class="flex flex-col gap-2">
      <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Search</label>
      <input
        type="search"
        placeholder="function, route, file..."
        value={searchStore.query()}
        onInput={(e) => searchStore.setQuery(e.currentTarget.value)}
        class="w-full rounded bg-gray-800 border border-gray-700 px-3 py-2 text-sm text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
      />
      <Show when={searchStore.results().length > 0}>
        <ul class="flex flex-col gap-1 max-h-48 overflow-y-auto">
          <For each={searchStore.results()}>
            {(node) => (
              <li>
                <button
                  class="w-full text-left text-xs text-gray-300 hover:text-indigo-300 px-2 py-1 rounded hover:bg-gray-800 truncate"
                  onClick={() => {
                    uiStore.setSelectedNodeId(node.id);
                    searchStore.saveRecent(searchStore.query());
                    graphStore.fetchTrace(node.id, "both", 10);
                  }}
                >
                  <span class="font-medium">{node.label}</span>
                  <span class="text-gray-500 ml-1">{node.service}</span>
                </button>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
};

export default Search;
