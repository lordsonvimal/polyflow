import { For, Show, createEffect, on, untrack } from "solid-js";
import Tree from "./explore/Tree";
import StackPanel from "./explore/StackPanel";
import { exploreStore, type ExploreTab } from "../stores/explore";
import { layoutPrefs } from "../stores/layoutPrefs";

const TABS: { id: ExploreTab; label: string }[] = [
  { id: "tree", label: "Tree" },
  { id: "stack", label: "Stack" },
];

// The default 280px panel is sized for tree rows, not Stack's per-service
// cards (name/lang/deps/node+edge distributions) — at that width the bar
// lists' label/bar/count run out of room. Widen once on switching into
// the tab; never fights a manual resize back down afterward (only tracks
// the tab, not panelWidth itself).
const STACK_MIN_WIDTH = 420;

export default function ExploreView() {
  createEffect(on(exploreStore.tab, (tab) => {
    if (tab === "stack" && untrack(layoutPrefs.panelWidth) < STACK_MIN_WIDTH) {
      layoutPrefs.setPanelWidth(STACK_MIN_WIDTH);
    }
  }));

  return (
    <div class="flex flex-col h-full min-h-0">
      <div data-testid="explore-tabs" class="flex items-center gap-1 px-2 pt-1 border-b border-neutral-800 shrink-0">
        <For each={TABS}>
          {(t) => (
            <button
              data-testid={`explore-tab-${t.id}`}
              class={`px-2 py-1 text-xs rounded-t ${
                exploreStore.tab() === t.id
                  ? "bg-neutral-900 text-white border border-b-0 border-neutral-800"
                  : "text-neutral-400 hover:text-neutral-300"
              }`}
              onClick={() => exploreStore.setTab(t.id)}
            >
              {t.label}
            </button>
          )}
        </For>
      </div>
      <div class="flex-1 min-h-0 overflow-hidden">
        <Show when={exploreStore.tab() === "tree"}><Tree /></Show>
        <Show when={exploreStore.tab() === "stack"}><StackPanel /></Show>
      </div>
    </div>
  );
}
