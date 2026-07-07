import { Component } from "solid-js";
import { searchStore } from "../stores/search";

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
    </div>
  );
};

export default Search;
