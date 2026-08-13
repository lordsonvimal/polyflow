import { Show } from "solid-js";
import { drawerStore } from "../stores/drawer";

export default function BottomDrawer() {
  const open = drawerStore.open;
  const setOpen = drawerStore.setOpen;

  return (
    <div
      data-testid="bottom-drawer"
      class="shrink-0 border-t border-neutral-800 dark:border-neutral-700 bg-neutral-950 transition-all"
      style={{ height: open() ? "200px" : "28px" }}
    >
      <div class="flex items-center px-2 h-7 gap-2 text-xs text-neutral-500">
        <button onClick={() => setOpen(!open())} class="hover:text-white">
          {open() ? "▼" : "▲"} Drawer
        </button>
        <Show when={open() && drawerStore.unresolvedFilter()}>
          {(f) => (
            <span data-testid="unresolved-filter-chip" class="text-amber-400">
              ⚠ Unresolved · {f().service} · {f().path || "/"}
            </span>
          )}
        </Show>
        <Show when={open()}>
          <button onClick={() => setOpen(false)} class="ml-auto hover:text-white">× close</button>
        </Show>
      </div>
    </div>
  );
}
