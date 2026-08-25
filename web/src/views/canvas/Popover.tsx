import { Show, createSignal, type JSX } from "solid-js";
import { Portal } from "solid-js/web";

// Shared trigger-button + floating-panel pattern used by FilterBar and
// LensBar so both collapse behind a single button regardless of screen
// size. Portals to <body> and positions from the toggle's own rect rather
// than relying on CSS absolute positioning, since ancestors may clip
// overflow (same reasoning Breadcrumbs' collapsed-menu popover uses).
export default function Popover(props: {
  testId: string;
  label: string;
  triggerClass?: string;
  children: JSX.Element;
}) {
  const [open, setOpen] = createSignal(false);
  const [pos, setPos] = createSignal({ top: 0, left: 0 });
  let toggleRef: HTMLButtonElement | undefined;

  function toggle() {
    if (!open() && toggleRef) {
      const rect = toggleRef.getBoundingClientRect();
      setPos({ top: rect.bottom + 4, left: rect.left });
    }
    setOpen((v) => !v);
  }

  return (
    <div class="relative flex items-center" data-testid={props.testId}>
      <button
        ref={toggleRef}
        data-testid={`${props.testId}-toggle`}
        class={
          props.triggerClass ??
          "px-2 py-0.5 rounded text-xs border border-neutral-800 text-neutral-400 hover:text-neutral-300 transition-colors"
        }
        onClick={toggle}
      >
        {props.label}
      </button>
      <Show when={open()}>
        <Portal>
          <div class="fixed inset-0 z-10" onClick={() => setOpen(false)} />
          <div
            data-testid={`${props.testId}-menu`}
            class="fixed z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs p-2 flex flex-col gap-2 max-h-[70vh] overflow-y-auto min-w-[220px]"
            style={{ top: `${pos().top}px`, left: `${pos().left}px` }}
            onClick={(e) => e.stopPropagation()}
          >
            {props.children}
          </div>
        </Portal>
      </Show>
    </div>
  );
}
