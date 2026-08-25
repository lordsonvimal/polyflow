import { For, Show, createSignal } from "solid-js";
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
  const max = () => props.maxVisible ?? 5;
  const overflowing = () => props.items.length > max();

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
      <div class="relative flex items-center" data-testid={props.testId}>
        <button
          data-testid={`${props.testId}-toggle`}
          class="px-2 py-0.5 rounded text-xs border border-neutral-800 text-neutral-400 hover:text-neutral-300 transition-colors"
          onClick={() => setOpen((v) => !v)}
        >
          {props.groupLabel} ({props.activeCount}/{props.items.length}) ▾
        </button>
        <Show when={open()}>
          <div
            data-testid={`${props.testId}-menu`}
            class="absolute top-full left-0 mt-1 z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs p-1.5 flex flex-col gap-1 max-h-64 overflow-y-auto min-w-[140px]"
          >
            <For each={props.items}>
              {(item) => <Chip label={item} active={props.isActive(item)} onClick={() => props.onToggle(item)} />}
            </For>
          </div>
        </Show>
      </div>
    </Show>
  );
}
