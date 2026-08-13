import { createSignal, Show, For } from "solid-js";

export type MenuItem = { id: string; label: string; handler: () => void };

// Registry: activities contribute menu items; unimplemented items are simply absent.
const contributions: { activityId: string; items: MenuItem[] }[] = [];

export function registerMenuItems(activityId: string, items: MenuItem[]) {
  const i = contributions.findIndex(c => c.activityId === activityId);
  if (i >= 0) contributions[i].items = items;
  else contributions.push({ activityId, items });
}

export function unregisterMenuItems(activityId: string) {
  const i = contributions.findIndex(c => c.activityId === activityId);
  if (i >= 0) contributions.splice(i, 1);
}

export function getMenuItems(): MenuItem[] {
  return contributions.flatMap(c => c.items);
}

type MenuState = { x: number; y: number; items: MenuItem[] } | null;
const [menu, setMenu] = createSignal<MenuState>(null);

export function openMenu(x: number, y: number) {
  const items = getMenuItems();
  if (items.length === 0) return;
  setMenu({ x, y, items });
}

export function closeMenu() { setMenu(null); }

export default function ContextMenu() {
  return (
    <Show when={menu()}>
      {(m) => (
        <>
          <div class="fixed inset-0 z-40" onClick={closeMenu} />
          <div
            data-testid="context-menu"
            class="fixed z-50 bg-neutral-900 border border-neutral-700 rounded shadow-lg py-1 min-w-40 text-sm"
            style={{ left: `${m().x}px`, top: `${m().y}px` }}
          >
            <For each={m().items}>
              {(item) => (
                <button
                  class="w-full text-left px-3 py-1 hover:bg-neutral-700 text-neutral-200"
                  onClick={() => { item.handler(); closeMenu(); }}
                >
                  {item.label}
                </button>
              )}
            </For>
          </div>
        </>
      )}
    </Show>
  );
}
