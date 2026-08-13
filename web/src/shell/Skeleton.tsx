import { For } from "solid-js";

const PULSE = "animate-pulse bg-neutral-800 rounded";

export function PanelSkeleton(props: { rows?: number }) {
  const rows = () => props.rows ?? 6;
  return (
    <div data-testid="skeleton-panel" class="p-3 flex flex-col gap-2">
      <For each={Array.from({ length: rows() })}>
        {(_, i) => <div class={PULSE} style={{ height: "10px", width: `${80 - (i() % 3) * 15}%` }} />}
      </For>
    </div>
  );
}

export function ListSkeleton(props: { rows?: number }) {
  const rows = () => props.rows ?? 5;
  return (
    <div data-testid="skeleton-list" class="flex flex-col gap-1 p-2">
      <For each={Array.from({ length: rows() })}>
        {() => <div class={`${PULSE} h-6 w-full`} />}
      </For>
    </div>
  );
}

export function TreeSkeleton(props: { rows?: number }) {
  const rows = () => props.rows ?? 6;
  return (
    <div data-testid="skeleton-tree" class="flex flex-col gap-1.5 p-2">
      <For each={Array.from({ length: rows() })}>
        {(_, i) => (
          <div class="flex items-center gap-1.5" style={{ "padding-left": `${(i() % 3) * 12}px` }}>
            <div class={`${PULSE} h-3 w-3 shrink-0`} />
            <div class={PULSE} style={{ height: "8px", width: `${60 - (i() % 3) * 10}%` }} />
          </div>
        )}
      </For>
    </div>
  );
}
