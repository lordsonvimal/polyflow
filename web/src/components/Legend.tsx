import { Component, For } from "solid-js";

const LANGS: [string, string][] = [
  ["go", "#00ADD8"],
  ["javascript", "#F7DF1E"],
  ["typescript", "#3178C6"],
  ["ruby", "#CC342D"],
  ["templ", "#7C3AED"],
];

const SHAPES: [string, string][] = [
  ["◼ handler / ◗ client", "HTTP"],
  ["◆ channel", "broker"],
  ["▮ datastore", "DB"],
  ["⬡ external service", "cloud SDK"],
  ["▢ boundary group", "framework/SDK (collapsed)"],
];

const Legend: Component = () => (
  <details class="text-xs">
    <summary class="text-xs font-semibold text-gray-400 uppercase tracking-wide cursor-pointer select-none">
      Legend
    </summary>
    <div class="mt-2 flex flex-col gap-2">
      <div class="flex flex-wrap gap-x-3 gap-y-1">
        <For each={LANGS}>
          {([lang, color]) => (
            <span class="flex items-center gap-1 text-gray-400">
              <span class="inline-block w-2.5 h-2.5 rounded-full" style={{ "background-color": color }} />
              {lang}
            </span>
          )}
        </For>
      </div>
      <div class="flex flex-col gap-0.5 text-gray-500">
        <For each={SHAPES}>
          {([shape, desc]) => (
            <span>
              <span class="text-gray-400">{shape}</span> — {desc}
            </span>
          )}
        </For>
      </div>
      <div class="flex flex-col gap-0.5 text-gray-500">
        <span><span class="text-gray-400">──▶</span> static / inferred edge</span>
        <span><span class="text-gray-400">┄┄▶</span> partial / unknown edge (opt-in)</span>
        <span><span class="text-pink-400">◯</span> trace root</span>
      </div>
    </div>
  </details>
);

export default Legend;
