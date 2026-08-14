import { createMemo, createSignal, createEffect, onMount, onCleanup, For, Show } from "solid-js";
import { treeStore, buildRows, type Row } from "../../stores/tree";
import { computeWindow } from "./virtualize";
import { selectionStore } from "../../stores/selection";
import { scopeStore } from "../../stores/scope";
import { canvasElementsStore } from "../../stores/canvasElements";
import { multiSelectStore } from "../../stores/multiSelect";
import { drawerStore } from "../../stores/drawer";
import { makeClickHandler, type Intent, type TargetRef } from "../../interaction/gestures";
import { registerMenuItems, unregisterMenuItems, openMenu } from "../../interaction/ContextMenu";
import { TreeSkeleton } from "../../shell/Skeleton";
import EmptyState from "../../shell/EmptyState";
import { notificationsStore } from "../../stores/notifications";
import { formatLocation, formatRange } from "../../lib/location";

const ROW_HEIGHT = 22;
const ACTIVITY_ID = "explore-tree";

const KIND_ICON: Record<string, string> = {
  service: "▣",
  folder: "▸",
  file: "▫",
  class: "▭",
  struct: "▭",
  function: "ƒ",
  method: "●",
  component: "◉",
  variable: "◖",
};

function iconFor(kind: string): string {
  return KIND_ICON[kind] ?? "•";
}

function scopeForRow(row: Row): Parameters<typeof scopeStore.push>[0] | null {
  switch (row.kind) {
    case "service":
      return { kind: "service", service: row.service };
    case "folder":
      return { kind: "folder", service: row.service, path: row.path ?? "" };
    case "file":
      return { kind: "file", service: row.service, path: row.path ?? "" };
    default:
      // Any symbol kind (class/function/method/component/variable/…)
      return row.nodeId ? { kind: "neighborhood", nodeId: row.nodeId, depth: 2 } : null;
  }
}

function locationLabel(row: Row): string | null {
  return formatLocation(row.file, row.line, row.endLine) || null;
}

function copyPath(row: Row): void {
  const loc = row.kind === "file" || row.kind === "folder" ? row.path ?? "" : locationLabel(row) ?? row.path ?? "";
  navigator.clipboard?.writeText(loc).catch(() => {});
  notificationsStore.add({ id: `copy-${Date.now()}`, kind: "info", message: `Copied: ${loc}` });
}

export default function Tree() {
  let scrollerRef: HTMLDivElement | undefined;
  const [scrollTop, setScrollTop] = createSignal(0);
  const [viewportHeight, setViewportHeight] = createSignal(480);

  onMount(() => {
    treeStore.loadServices();
    treeStore.start();
    if (scrollerRef && typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(() => setViewportHeight(scrollerRef!.clientHeight || 480));
      ro.observe(scrollerRef);
      onCleanup(() => ro.disconnect());
    }
    onCleanup(() => treeStore.stop());
  });

  const rows = createMemo(() => buildRows(treeStore.services(), treeStore.entries(), treeStore.expanded()));
  const win = createMemo(() => computeWindow(scrollTop(), viewportHeight(), ROW_HEIGHT, rows().length));
  const visibleRows = createMemo(() => rows().slice(win().start, win().end));

  // Canvas → tree: a selection made anywhere (canvas tap, palette) reveals
  // and highlights the owning row, auto-expanding ancestors.
  createEffect(() => {
    const sel = selectionStore.selection();
    if (sel && sel.kind === "node") treeStore.reveal(sel.id);
  });

  // Scroll the highlighted row into view once it's part of the flattened list.
  createEffect(() => {
    const hk = treeStore.highlightedKey();
    if (!hk || !scrollerRef) return;
    const idx = rows().findIndex((r) => r.key === hk);
    if (idx < 0) return;
    const rowTop = idx * ROW_HEIGHT;
    const viewTop = scrollTop();
    const viewBottom = viewTop + viewportHeight();
    if (rowTop < viewTop || rowTop + ROW_HEIGHT > viewBottom) {
      scrollerRef.scrollTop = rowTop;
      setScrollTop(rowTop);
    }
  });

  onCleanup(() => unregisterMenuItems(ACTIVITY_ID));

  function toggleContainer(row: Row) {
    treeStore.toggleExpand(row.key);
    if (row.kind === "service" && treeStore.expanded().has(row.key)) {
      treeStore.loadService(row.service);
    }
  }

  function select(row: Row) {
    if (!row.nodeId) {
      // Folders/services carry no backing graph node — expand is their
      // meaningful "select" action rather than a silent no-op.
      toggleContainer(row);
      return;
    }
    selectionStore.setSelection({ kind: "node", id: row.nodeId });
    if (!canvasElementsStore.has(row.nodeId)) {
      const target = scopeForRow(row);
      if (target) {
        notificationsStore.add({
          id: `open-scope-${Date.now()}`,
          kind: "info",
          message: `${row.name} isn't in the current view — opening its scope.`,
        });
        scopeStore.push(target);
      }
    }
  }

  function drill(row: Row) {
    const target = scopeForRow(row);
    if (row.hasChildren) treeStore.expandKeys([row.key]);
    if (target) scopeStore.push(target);
    if (row.nodeId) selectionStore.setSelection({ kind: "node", id: row.nodeId });
  }

  function menu(row: Row, x: number, y: number) {
    const items = [];
    if (row.hasChildren) {
      items.push({
        id: "toggle",
        label: treeStore.expanded().has(row.key) ? "Collapse" : "Expand",
        handler: () => toggleContainer(row),
      });
    }
    const target = scopeForRow(row);
    if (target) {
      items.push({ id: "open-scope", label: "Open scope", handler: () => scopeStore.push(target) });
    }
    if (row.path || row.file) {
      items.push({ id: "copy-path", label: "Copy path", handler: () => copyPath(row) });
    }
    registerMenuItems(ACTIVITY_ID, items);
    openMenu(x, y);
  }

  function onIntent(row: Row, intent: Intent) {
    switch (intent.type) {
      case "select":
        select(row);
        break;
      case "drill":
        drill(row);
        break;
      case "menu":
        menu(row, intent.x, intent.y);
        break;
      case "hoverTarget":
        selectionStore.setHoverTarget(
          intent.target
            ? { kind: "node", id: row.nodeId ?? row.key, label: row.name, file: row.file, line: row.line, end_line: row.endLine }
            : null,
        );
        break;
      case "multiAdd":
        // UF.4: only real graph nodes (row.nodeId set) can join a group —
        // a bare folder/service row's synthetic key isn't a node id.
        if (row.nodeId) multiSelectStore.toggle(row.nodeId);
        break;
      default:
        break;
    }
  }

  function targetFor(row: Row): TargetRef {
    return { kind: "node", id: row.nodeId ?? row.key };
  }

  function badgeCount(row: Row): number {
    if (row.kind === "folder" && row.path !== undefined) return treeStore.unresolvedCount(row.service, "folder", row.path);
    if (row.kind === "file" && row.path !== undefined) return treeStore.unresolvedCount(row.service, "file", row.path);
    return 0;
  }

  function onBadgeClick(row: Row, e: MouseEvent) {
    e.stopPropagation();
    drawerStore.openUnresolvedFor(row.service, row.path ?? "");
  }

  return (
    <div data-testid="tree-explorer" class="flex flex-col h-full min-h-0">
      <Show when={treeStore.servicesLoading()}>
        <TreeSkeleton />
      </Show>
      <Show when={!treeStore.servicesLoading() && treeStore.servicesError()}>
        <EmptyState message="Failed to load services" detail={treeStore.servicesError()} icon="⚠" />
      </Show>
      <Show when={!treeStore.servicesLoading() && !treeStore.servicesError() && treeStore.services().length === 0}>
        <EmptyState message="No services indexed yet" detail="Run an index to populate the tree." icon="◆" />
      </Show>
      <Show when={!treeStore.servicesLoading() && treeStore.services().length > 0}>
        <div
          ref={scrollerRef}
          data-testid="tree-scroller"
          class="flex-1 min-h-0 overflow-y-auto text-sm"
          onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
        >
          <div style={{ height: `${win().topPad}px` }} />
          <For each={visibleRows()}>
            {(row) => {
              const handlers = makeClickHandler(targetFor(row), (i) => onIntent(row, i));
              const isLoading = row.kind === "__loading__";
              const isError = row.kind === "__error__";
              return (
                <div
                  data-testid="tree-row"
                  data-row-key={row.key}
                  data-kind={row.kind}
                  class={`flex items-center gap-1 px-1 cursor-pointer select-none whitespace-nowrap
                    ${treeStore.highlightedKey() === row.key ? "bg-neutral-700" : "hover:bg-neutral-800"}`}
                  style={{ height: `${ROW_HEIGHT}px`, "padding-left": `${6 + row.depth * 14}px` }}
                  {...(isLoading || isError ? {} : handlers)}
                >
                  <Show when={isLoading}>
                    <span class="text-neutral-600 text-xs">loading…</span>
                  </Show>
                  <Show when={isError}>
                    <span class="text-red-400 text-xs">{row.name}</span>
                  </Show>
                  <Show when={!isLoading && !isError}>
                    <Show
                      when={row.hasChildren}
                      fallback={<span class="w-3 shrink-0" />}
                    >
                      <button
                        data-testid="tree-row-toggle"
                        class="w-3 shrink-0 text-neutral-500 text-[10px] hover:text-white"
                        onClick={(e) => {
                          e.stopPropagation();
                          toggleContainer(row);
                        }}
                      >
                        {treeStore.expanded().has(row.key) ? "▾" : "▸"}
                      </button>
                    </Show>
                    <span class="shrink-0 text-neutral-400">{iconFor(row.kind)}</span>
                    <span class="text-neutral-200 truncate">{row.name}</span>
                    <Show when={row.kind !== "folder" && row.kind !== "service" && row.kind !== "file" && row.line}>
                      <span class="text-neutral-600 text-xs shrink-0">
                        {formatRange(row.line, row.endLine)}
                      </span>
                    </Show>
                    <Show when={badgeCount(row) > 0}>
                      <button
                        data-testid="unresolved-badge"
                        class="ml-1 text-amber-400 text-xs shrink-0 hover:text-amber-300"
                        onClick={(e) => onBadgeClick(row, e)}
                        title={`${badgeCount(row)} unresolved reference(s)`}
                      >
                        ⚠ {badgeCount(row)}
                      </button>
                    </Show>
                  </Show>
                </div>
              );
            }}
          </For>
          <div style={{ height: `${win().bottomPad}px` }} />
        </div>
      </Show>
    </div>
  );
}
