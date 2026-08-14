import { createSignal, onMount, createEffect, Show, createMemo, For } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";
import { paletteStore } from "../stores/palette";
import { apiFetchJSON } from "../lib/apiFetch";
import Breadcrumbs from "./Breadcrumbs";
import LensBar from "../views/canvas/LensBar";
import { NO_CANVAS } from "../views/canvas/CanvasHost";
import { scopeStore } from "../stores/scope";
import { pathFinderStore } from "../stores/pathFinder";
import { pinboardStore } from "../stores/pinboard";
import { selectionStore } from "../stores/selection";
import { displayLabel } from "../lib/location";

export default function TopBar() {
  const [stats, setStats] = createSignal("--n/--e");
  const isCanvasPage = createMemo(() => !NO_CANVAS.has(scopeStore.stack().at(-1)?.kind ?? "search"));

  onMount(async () => {
    try {
      const d = await apiFetchJSON<{ nodes: number; edges: number }>("/api/stats", { silent: true });
      setStats(`${d.nodes}n/${d.edges}e`);
    } catch {
      setStats("--n/--e");
    }
  });

  createEffect(() => {
    if (layoutPrefs.theme() === "dark") {
      document.documentElement.classList.add("dark");
    } else {
      document.documentElement.classList.remove("dark");
    }
  });

  return (
    <header
      data-testid="top-bar"
      class="flex items-center gap-3 px-3 h-10 shrink-0 border-b border-neutral-800 dark:border-neutral-700 bg-neutral-950 text-sm"
    >
      <span class="font-semibold text-white">◆ polyflow</span>
      <Breadcrumbs />
      <Show when={isCanvasPage()}>
        <LensBar />
      </Show>
      {/* UF.7: pin tray — chips for pinboard pins, shown any time at least
          one pin exists (badges from 1; the canvas fade only starts at 2). */}
      <Show when={pinboardStore.pins().length > 0}>
        <div data-testid="pin-tray" class="flex items-center gap-1">
          <span class="text-neutral-500">pins:</span>
          <For each={pinboardStore.pins()}>
            {(p) => (
              <span
                data-testid="pin-chip"
                class="flex items-center gap-1 bg-neutral-800 border border-neutral-700 rounded px-2 py-0.5 text-xs text-neutral-300"
              >
                <button
                  class="truncate max-w-[160px] hover:text-white"
                  title={p.label}
                  onClick={() => selectionStore.setSelection({ kind: "node", id: p.id })}
                >
                  {displayLabel(p.label)}
                </button>
                <button class="text-neutral-500 hover:text-white" onClick={() => pinboardStore.unpin(p.id)}>
                  ×
                </button>
              </span>
            )}
          </For>
          <Show when={pinboardStore.active()}>
            <button
              data-testid="pin-tray-view-as-lane"
              class="text-xs text-indigo-300 hover:text-indigo-200"
              onClick={() => scopeStore.push({
                kind: "flow",
                flow: { kind: "pins", ids: pinboardStore.pins().map((p) => p.id) },
              })}
            >
              View as flow lane
            </button>
          </Show>
          <button
            data-testid="pin-tray-clear"
            class="text-xs text-neutral-500 hover:text-white"
            onClick={() => pinboardStore.clear()}
          >
            [clear all]
          </button>
        </div>
      </Show>
      <Show when={pathFinderStore.startNode()}>
        {(start) => (
          <span
            data-testid="path-start-chip"
            class="flex items-center gap-1 bg-neutral-800 border border-neutral-700 rounded px-2 py-0.5 text-xs text-neutral-300"
          >
            <span class="truncate max-w-[160px]" title={start().label}>A: {displayLabel(start().label)}</span>
            <button class="text-neutral-500 hover:text-white" onClick={() => pathFinderStore.clearStart()}>
              ×
            </button>
          </span>
        )}
      </Show>
      <div class="ml-auto flex items-center gap-2">
        <span class="text-xs text-neutral-500 font-mono">{stats()}</span>
        <button class="text-xs text-neutral-500 hover:text-white border border-neutral-700 rounded px-2 py-0.5" disabled>
          Index ▸
        </button>
        <button class="text-xs text-neutral-500 hover:text-white border border-neutral-700 rounded px-2 py-0.5" disabled>
          Share ▾
        </button>
        <button
          class="text-xs text-neutral-500 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
          onClick={() => layoutPrefs.setTheme(layoutPrefs.theme() === "dark" ? "light" : "dark")}
        >
          {layoutPrefs.theme() === "dark" ? "☀" : "☾"}
        </button>
        <button
          class="text-xs text-neutral-500 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
          onClick={() => paletteStore.open()}
        >
          ⌘K
        </button>
      </div>
    </header>
  );
}
