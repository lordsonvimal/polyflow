import { Component, For } from "solid-js";
import { EDGE_LEGEND, LANG_COLORS, NODE_TYPE_STYLES } from "../lib/styles";

// The legend is generated from the same tables that build the Cytoscape
// stylesheet (lib/styles.ts), so every rendered shape/color has an entry.
const Legend: Component = () => (
  <details class="text-xs">
    <summary class="text-xs font-semibold text-gray-400 uppercase tracking-wide cursor-pointer select-none">
      Legend
    </summary>
    <div class="mt-2 flex flex-col gap-2">
      <div class="flex flex-wrap gap-x-3 gap-y-1">
        <For each={LANG_COLORS}>
          {([lang, color]) => (
            <span class="flex items-center gap-1 text-gray-400">
              <span class="inline-block w-2.5 h-2.5 rounded-full" style={{ "background-color": color }} />
              {lang}
            </span>
          )}
        </For>
      </div>
      <div class="flex flex-col gap-0.5 text-gray-500">
        <For each={NODE_TYPE_STYLES}>
          {({ type, color, glyph, desc }) => (
            <span>
              <span class="text-gray-300" style={color ? { color } : {}}>
                {glyph}
              </span>{" "}
              <span class="text-gray-400">{type.replace(/_/g, " ")}</span> — {desc}
            </span>
          )}
        </For>
      </div>
      <div class="flex flex-col gap-0.5 text-gray-500">
        <For each={EDGE_LEGEND}>
          {({ glyph, color, desc }) => (
            <span>
              <span class="text-gray-300" style={color ? { color } : {}}>
                {glyph}
              </span>{" "}
              — {desc}
            </span>
          )}
        </For>
      </div>
    </div>
  </details>
);

export default Legend;
