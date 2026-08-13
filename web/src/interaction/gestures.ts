import { selectionStore } from "../stores/selection";
import { scopeStore } from "../stores/scope";

export type TargetRef = { kind: "node" | "edge"; id: string };

export type Intent =
  | { type: "select"; target: TargetRef }
  | { type: "drill"; target: TargetRef }
  | { type: "menu"; target: TargetRef; x: number; y: number }
  | { type: "hoverTarget"; target: TargetRef | null }
  | { type: "multiAdd"; target: TargetRef }
  | { type: "escape" };

// Shared intent handler — writes to stores. Both tree rows and canvas nodes call this.
export function handleIntent(intent: Intent): void {
  switch (intent.type) {
    case "select":
      selectionStore.setSelection(intent.target);
      break;
    case "drill":
      // Default drill target for a plain symbol node (function, class, ...)
      // with no more specific container scope to expand into. CanvasHost
      // (UN.1) intercepts drill on folder/file/service compounds and
      // boundary stubs before this generic handler runs.
      scopeStore.push({ kind: "neighborhood", nodeId: intent.target.id, depth: 1 });
      break;
    case "hoverTarget":
      selectionStore.setHoverTarget(intent.target);
      break;
    case "escape":
      scopeStore.handleEsc();
      break;
    case "menu":
    case "multiAdd":
      // Callers handle these (menu → openContextMenu, multiAdd → plan 12)
      break;
  }
}

// DOM click handler with 300ms double-click disambiguation.
// Returns event handlers to spread onto any interactive element.
export function makeClickHandler(
  target: TargetRef,
  onIntent: (i: Intent) => void
): {
  onClick: (e: MouseEvent) => void;
  onContextMenu: (e: MouseEvent) => void;
  onMouseEnter: () => void;
  onMouseLeave: () => void;
} {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return {
    onClick(e) {
      if (e.shiftKey) { onIntent({ type: "multiAdd", target }); return; }
      if (timer !== undefined) {
        clearTimeout(timer);
        timer = undefined;
        onIntent({ type: "drill", target });
      } else {
        timer = setTimeout(() => {
          timer = undefined;
          onIntent({ type: "select", target });
        }, 300);
      }
    },
    onContextMenu(e) {
      e.preventDefault();
      clearTimeout(timer);
      timer = undefined;
      onIntent({ type: "menu", target, x: e.clientX, y: e.clientY });
    },
    onMouseEnter() { onIntent({ type: "hoverTarget", target }); },
    onMouseLeave() { onIntent({ type: "hoverTarget", target: null }); },
  };
}

// Cytoscape event adapter — same 300ms disambiguation, same intents.
// cy is typed as `any` to avoid a hard cytoscape dep here; US.3 passes the real instance.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function wireCytoscape(cy: any, onIntent: (i: Intent) => void): () => void {
  const timers = new Map<string, ReturnType<typeof setTimeout>>();

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function ref(el: any): TargetRef {
    return { kind: el.isNode() ? "node" : "edge", id: el.id() };
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function onTap(e: any) {
    const el = e.target;
    if (el === cy) {
      onIntent({ type: "escape" });
      return;
    }
    const t = ref(el);
    const id: string = el.id();
    if ((e.originalEvent as MouseEvent)?.shiftKey) {
      onIntent({ type: "multiAdd", target: t });
      return;
    }
    if (timers.has(id)) {
      clearTimeout(timers.get(id));
      timers.delete(id);
      onIntent({ type: "drill", target: t });
    } else {
      const h = setTimeout(() => { timers.delete(id); onIntent({ type: "select", target: t }); }, 300);
      timers.set(id, h);
    }
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function onCxttap(e: any) {
    const el = e.target;
    if (el === cy) return;
    const pos = e.originalEvent as MouseEvent;
    onIntent({ type: "menu", target: ref(el), x: pos.clientX, y: pos.clientY });
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function onMouseover(e: any) {
    const el = e.target;
    if (el === cy) return;
    onIntent({ type: "hoverTarget", target: ref(el) });
  }

  function onMouseout() {
    onIntent({ type: "hoverTarget", target: null });
  }

  cy.on("tap", onTap);
  cy.on("cxttap", onCxttap);
  cy.on("mouseover", onMouseover);
  cy.on("mouseout", onMouseout);

  return () => {
    cy.off("tap", onTap);
    cy.off("cxttap", onCxttap);
    cy.off("mouseover", onMouseover);
    cy.off("mouseout", onMouseout);
    timers.forEach(clearTimeout);
    timers.clear();
  };
}
