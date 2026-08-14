import { scopeStore } from "../stores/scope";
import { paletteStore } from "../stores/palette";
import { selectionStore } from "../stores/selection";
import { multiSelectStore } from "../stores/multiSelect";
import { contextCopyStore } from "../stores/contextCopy";
import { selectionCopySource } from "../views/context/copy";
import { pinboardStore } from "../stores/pinboard";

export type KeyBinding = {
  key: string;       // matches KeyboardEvent.key or "Meta+k" compound
  display: string;   // human-readable for the shortcut sheet (plan 13)
  description: string;
  handler: () => void;
};

// Single source of truth for all keyboard shortcuts.
// Plans 11–13 add entries; this table is read by the docs shortcut sheet.
export const KEY_BINDINGS: KeyBinding[] = [
  {
    key: "Escape",
    display: "Esc",
    description: "Close detail / clear selection / pop isolation / pop scope",
    handler: () => scopeStore.handleEsc(),
  },
  {
    key: "Meta+k",
    display: "⌘K",
    description: "Open command palette",
    handler: () => paletteStore.toggle(),
  },
  {
    key: "/",
    display: "/",
    description: "Open command palette",
    handler: () => paletteStore.toggle(),
  },
  {
    key: "p",
    display: "p",
    description: "Pin/unpin hovered or selected node to the pinboard",
    handler: () => {
      // Hover wins (mirrors gesture-grammar precedent elsewhere: a hover
      // target is the more specific "the thing under the cursor right now"
      // signal) — hoverTarget carries a real label; a bare selection falls
      // back to its id (DetailHost's pinboard button does the same).
      const hover = selectionStore.hoverTarget();
      if (hover && hover.kind === "node") {
        pinboardStore.toggle({ id: hover.id, label: hover.label ?? hover.id });
        return;
      }
      const sel = selectionStore.selection();
      if (sel && sel.kind === "node") {
        pinboardStore.toggle({ id: sel.id, label: sel.id });
      }
    },
  },
  {
    key: "[",
    display: "[",
    description: "Peek one hop upstream from selection",
    handler: () => {}, // plan 12 UF.8
  },
  {
    key: "]",
    display: "]",
    description: "Peek one hop downstream from selection",
    handler: () => {}, // plan 12 UF.8
  },
  {
    key: "Meta+Shift+C",
    display: "⌘⇧C",
    description: "Copy context for current selection",
    handler: () => {
      const top = scopeStore.stack().at(-1);
      const source = selectionCopySource(selectionStore.selection(), multiSelectStore.ids(), top);
      if (source) contextCopyStore.copy(source);
    },
  },
];

// Attaches keyboard handlers to any element (typically window).
// Returns a cleanup function.
export function registerKeys(element: HTMLElement | Window): () => void {
  const handler = (e: KeyboardEvent) => {
    // Skip when typing in an input/textarea
    const tag = (e.target as HTMLElement)?.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA") {
      if (e.key !== "Escape") return;
    }
    const compound = [
      e.metaKey && "Meta",
      e.shiftKey && "Shift",
      e.key,
    ].filter(Boolean).join("+");
    const binding = KEY_BINDINGS.find(b => b.key === compound || b.key === e.key);
    if (binding) {
      e.preventDefault();
      binding.handler();
    }
  };
  element.addEventListener("keydown", handler as EventListener);
  return () => element.removeEventListener("keydown", handler as EventListener);
}
