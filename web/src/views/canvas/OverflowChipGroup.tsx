import { For, Show, createSignal } from "solid-js";
import { Portal } from "solid-js/web";
import { Chip } from "./Chip";

// Below `maxVisible` items, renders the exact same inline chip row FilterBar
// always has (so low service counts are pixel-identical to before this
// existed). Above the threshold, collapses into a single toggle + popover so
// an unbounded axis (e.g. fleet services) can't crowd out the bounded chip
// groups sharing FilterBar's one scrollable strip.
export default function OverflowChipGroup(props: {
  testId: string;
  groupLabel: string;
  items: readonly string[];
  isActive: (item: string) => boolean;
  onToggle: (item: string) => void;
  activeCount: number;
  maxVisible?: number;
}) {
  const [open, setOpen] = createSignal(false);
  const [pos, setPos] = createSignal({ top: 0, left: 0 });
  const max = () => props.maxVisible ?? 5;
  const overflowing = () => props.items.length > max();
  let toggleRef: HTMLButtonElement | undefined;

  // FilterBar's row is overflow-x-auto, which per the CSS spec forces
  // overflow-y: auto too — clipping an absolutely-positioned menu nested
  // inside it instead of letting it float above the page. Portal to <body>
  // and position from the toggle button's own rect instead (same fix as
  // Breadcrumbs.tsx's collapsed-menu popover).
  function toggle() {
    if (!open() && toggleRef) {
      const rect = toggleRef.getBoundingClientRect();
      setPos({ top: rect.bottom + 4, left: rect.left });
    }
    setOpen((v) => !v);
  }

  return (
    <Show
      when={overflowing()}
      fallback={
        <div class="flex items-center gap-1" data-testid={props.testId}>
          <For each={props.items}>
            {(item) => <Chip label={item} active={props.isActive(item)} onClick={() => props.onToggle(item)} />}
          </For>
        </div>
      }
    >
      <div class="flex items-center" data-testid={props.testId}>
        <button
          ref={toggleRef}
          data-testid={`${props.testId}-toggle`}
          class="px-2 py-0.5 rounded text-xs border border-neutral-800 text-neutral-400 hover:text-neutral-300 transition-colors"
          onClick={toggle}
        >
          {props.groupLabel} ({props.activeCount}/{props.items.length}) ▾
        </button>
        <Show when={open()}>
          <Portal>
            <div class="fixed inset-0 z-10" onClick={() => setOpen(false)} />
            <div
              data-testid={`${props.testId}-menu`}
              class="fixed z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs p-1.5 flex flex-col gap-1 max-h-64 overflow-y-auto min-w-[140px]"
              style={{ top: `${pos().top}px`, left: `${pos().left}px` }}
            >
              <For each={props.items}>
                {(item) => <Chip label={item} active={props.isActive(item)} onClick={() => props.onToggle(item)} />}
              </For>
            </div>
          </Portal>
        </Show>
      </div>
    </Show>
  );
}
