import { createSignal, Show, onMount, onCleanup } from "solid-js";
import { selectionStore } from "../stores/selection";
import { formatLocation } from "../lib/location";

export default function HoverTooltip() {
  const [pos, setPos] = createSignal({ x: 0, y: 0 });

  onMount(() => {
    const move = (e: MouseEvent) => setPos({ x: e.clientX + 14, y: e.clientY + 10 });
    window.addEventListener("mousemove", move);
    onCleanup(() => window.removeEventListener("mousemove", move));
  });

  return (
    <Show when={selectionStore.hoverTarget()}>
      {(t) => {
        const target = t();
        const filePart = formatLocation(target.file, target.line, target.end_line) || null;
        return (
          <div
            data-testid="hover-tooltip"
            class="fixed z-50 pointer-events-none px-2 py-1 bg-neutral-800 border border-neutral-700 rounded text-xs text-neutral-200 max-w-xs"
            style={{ left: `${pos().x}px`, top: `${pos().y}px` }}
          >
            <span class="font-medium">{target.label ?? target.id}</span>
            <span class="text-neutral-400 ml-1">{target.kind}</span>
            {filePart && <span class="text-neutral-500 ml-1">{filePart}</span>}
          </div>
        );
      }}
    </Show>
  );
}
