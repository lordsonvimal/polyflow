// UO.5: Explorer's "Saved Views" tab — list of named ViewState snapshots.
// Click applies (decode + push into scopeStore, with stale-id fallback);
// right-click offers rename/delete, mirroring Tree.tsx's row menu pattern.
import { For, Show, onMount, onCleanup, createSignal } from "solid-js";
import { savedViewsStore, type SavedView } from "../../stores/savedViews";
import { registerMenuItems, unregisterMenuItems, openMenu } from "../../interaction/ContextMenu";
import EmptyState from "../../shell/EmptyState";

const ACTIVITY_ID = "explore-saved-views";

function formatDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export default function SavedViewsPanel() {
  const [renaming, setRenaming] = createSignal<number | null>(null);
  const [renameValue, setRenameValue] = createSignal("");

  onMount(() => void savedViewsStore.list());
  onCleanup(() => unregisterMenuItems(ACTIVITY_ID));

  function menu(view: SavedView, x: number, y: number) {
    registerMenuItems(ACTIVITY_ID, [
      {
        id: "rename",
        label: "Rename",
        handler: () => {
          setRenaming(view.id);
          setRenameValue(view.name);
        },
      },
      {
        id: "delete",
        label: "Delete",
        handler: () => void savedViewsStore.remove(view.id),
      },
    ]);
    openMenu(x, y);
  }

  function commitRename(view: SavedView) {
    const name = renameValue().trim();
    setRenaming(null);
    if (name && name !== view.name) void savedViewsStore.rename(view.id, name);
  }

  return (
    <div data-testid="saved-views-panel" class="flex flex-col h-full min-h-0 overflow-y-auto text-sm">
      <Show when={savedViewsStore.views().length === 0 && !savedViewsStore.loading()}>
        <EmptyState message="No saved views yet — use the ★ button in the top bar to save the current canvas." />
      </Show>
      <For each={savedViewsStore.views()}>
        {(view) => (
          <div
            data-testid="saved-view-row"
            class="group flex items-center gap-2 px-2 py-1.5 border-b border-neutral-800 hover:bg-neutral-900 cursor-pointer"
            onClick={() => renaming() === null && void savedViewsStore.apply(view)}
            onContextMenu={(e) => {
              e.preventDefault();
              menu(view, e.clientX, e.clientY);
            }}
          >
            <span class="text-neutral-500">☆</span>
            <Show
              when={renaming() === view.id}
              fallback={
                <span class="flex-1 truncate text-neutral-200" title={view.name}>
                  {view.name}
                </span>
              }
            >
              <input
                data-testid="saved-view-rename-input"
                class="flex-1 bg-neutral-800 border border-neutral-700 rounded px-1 text-neutral-100 outline-none"
                value={renameValue()}
                autofocus
                onClick={(e) => e.stopPropagation()}
                onInput={(e) => setRenameValue(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") commitRename(view);
                  if (e.key === "Escape") setRenaming(null);
                }}
                onBlur={() => commitRename(view)}
              />
            </Show>
            <span class="text-xs text-neutral-500 shrink-0">{formatDate(view.created_at)}</span>
          </div>
        )}
      </For>
    </div>
  );
}
