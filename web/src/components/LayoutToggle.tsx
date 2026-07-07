import { Component } from "solid-js";
import { uiStore } from "../stores/ui";

const LAYOUTS = ["dagre", "fcose", "circle", "grid"] as const;

const LayoutToggle: Component = () => {
  return (
    <div class="flex flex-col gap-2">
      <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Layout</label>
      <div class="flex flex-wrap gap-1">
        {LAYOUTS.map((l) => (
          <button
            onClick={() => uiStore.setLayout(l)}
            class={`px-2 py-1 rounded text-xs ${
              uiStore.layout() === l
                ? "bg-indigo-600 text-white"
                : "bg-gray-800 text-gray-300 hover:bg-gray-700"
            }`}
          >
            {l}
          </button>
        ))}
      </div>
    </div>
  );
};

export default LayoutToggle;
