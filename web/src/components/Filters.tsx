import { Component, For } from "solid-js";
import { uiStore } from "../stores/ui";

const NODE_TYPES = [
  "http_handler",
  "http_client",
  "function",
  "route",
  "worker",
  "publisher",
  "subscriber",
  "templ_element",
];

const Filters: Component = () => {
  return (
    <div class="flex flex-col gap-2">
      <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Filter by type</label>
      <div class="flex flex-col gap-1">
        <For each={NODE_TYPES}>
          {(type) => (
            <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer">
              <input
                type="checkbox"
                checked={uiStore.activeFilters().includes(type)}
                onChange={(e) => {
                  if (e.currentTarget.checked) {
                    uiStore.addFilter(type);
                  } else {
                    uiStore.removeFilter(type);
                  }
                }}
                class="accent-indigo-500"
              />
              {type}
            </label>
          )}
        </For>
      </div>
    </div>
  );
};

export default Filters;
