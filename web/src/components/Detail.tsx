import { Component, Show } from "solid-js";
import { uiStore } from "../stores/ui";
import { graphStore } from "../stores/graph";

const Detail: Component = () => {
  const node = () => {
    const id = uiStore.selectedNodeId();
    if (!id) return null;
    return graphStore.nodes().find((n) => n.id === id) ?? null;
  };

  return (
    <div class="p-4">
      <Show when={node()} fallback={<p class="text-sm text-gray-500">Select a node to see details.</p>}>
        {(n) => (
          <div class="flex flex-col gap-3">
            <h2 class="text-sm font-semibold text-indigo-300 truncate">{n().label}</h2>
            <dl class="text-xs text-gray-300 space-y-1">
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Type</dt><dd>{n().type}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Service</dt><dd>{n().service}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">File</dt><dd class="truncate">{n().file}</dd></div>
              <div class="flex gap-2"><dt class="text-gray-500 w-16 shrink-0">Line</dt><dd>{n().line}</dd></div>
            </dl>
          </div>
        )}
      </Show>
    </div>
  );
};

export default Detail;
