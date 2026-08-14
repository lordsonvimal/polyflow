import { createSignal, onMount, createEffect, Show, createMemo } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";
import { paletteStore } from "../stores/palette";
import { apiFetchJSON } from "../lib/apiFetch";
import Breadcrumbs from "./Breadcrumbs";
import LensBar from "../views/canvas/LensBar";
import { NO_CANVAS } from "../views/canvas/CanvasHost";
import { scopeStore } from "../stores/scope";

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
