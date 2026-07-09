import { Component, For, Show } from "solid-js";
import { uiStore } from "../stores/ui";
import { allNodeTypes, allServices } from "../stores/derived";
import { CONFIDENCE_LEVELS } from "../lib/confidence";

// Filters actually shape the rendered graph (via stores/derived):
// - services and node types are derived from the indexed data, all shown by
//   default, individually hideable;
// - confidence defaults to static+inferred; partial/unknown are opt-in and
//   render dashed/dimmed when enabled.
const Filters: Component = () => {
  const checkbox = (checked: boolean, onChange: () => void, label: string, muted = false) => (
    <label class={`flex items-center gap-2 text-xs cursor-pointer ${muted ? "text-gray-500" : "text-gray-300"}`}>
      <input type="checkbox" checked={checked} onChange={onChange} class="accent-indigo-500" />
      {label}
    </label>
  );

  return (
    <div class="flex flex-col gap-4 overflow-y-auto">
      <div class="flex flex-col gap-1.5">
        <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Confidence</label>
        <For each={[...CONFIDENCE_LEVELS]}>
          {(level) =>
            checkbox(
              uiStore.activeConfidence().includes(level),
              () => uiStore.toggleConfidence(level),
              level === "partial" || level === "unknown" ? `${level} (opt-in, dashed)` : level,
              level === "partial" || level === "unknown"
            )
          }
        </For>
      </div>

      <Show when={allServices().length > 1}>
        <div class="flex flex-col gap-1.5">
          <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Services</label>
          <For each={allServices()}>
            {(svc) =>
              checkbox(
                !uiStore.hiddenServices().includes(svc),
                () => uiStore.toggleHiddenService(svc),
                svc
              )
            }
          </For>
        </div>
      </Show>

      <div class="flex flex-col gap-1.5">
        <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Node types</label>
        <For each={allNodeTypes()}>
          {(type) =>
            checkbox(
              !uiStore.hiddenTypes().includes(type),
              () => uiStore.toggleHiddenType(type),
              type
            )
          }
        </For>
      </div>

      <div class="flex flex-col gap-1.5">
        <label class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Boundaries</label>
        <div class="flex gap-1">
          <button
            class="px-2 py-1 rounded text-xs bg-gray-800 text-gray-300 hover:bg-gray-700 cursor-pointer"
            onClick={() => uiStore.collapseAllBoundaries()}
          >
            Collapse all
          </button>
        </div>
        <p class="text-[10px] text-gray-600 leading-snug">
          Framework/SDK call sites (Gin, AWS SDK, bunny…) are grouped into one node per package.
          Double-click a group to expand it.
        </p>
      </div>
    </div>
  );
};

export default Filters;
