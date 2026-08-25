import { For, Show, createMemo, createSignal } from "solid-js";
import { scopeStore, type Scope } from "../stores/scope";
import { flowRefLabel } from "../views/canvas/scopes/flow";
import { contextCopyStore } from "../stores/contextCopy";
import { flowRefToSource, type CopySource } from "../views/context/copy";
import { displayLabel } from "../lib/location";

// UF.5: the breadcrumb menu's "Copy context" only has a UB.6 reading for
// the current (last) scope — everything on screen right now — since
// ancestor crumbs aren't independently rendered. group/flow scopes route to
// their own element kind rather than the generic "scope" (on-canvas ids)
// fallback.
function copySourceForScope(scope: Scope): CopySource {
  if (scope.kind === "group") return { kind: "group", ids: scope.nodeIds };
  if (scope.kind === "flow") return flowRefToSource(scope.flow, []);
  return { kind: "scope" };
}

function crumbTitle(scope: Scope): string | undefined {
  if (scope.kind === "folder" || scope.kind === "file") return scope.path;
  return undefined;
}

function crumbLabel(scope: Scope): string {
  switch (scope.kind) {
    case "overview": return "overview";
    case "search": return "search";
    case "service": return scope.service;
    case "folder": return displayLabel(scope.path);
    case "file": return displayLabel(scope.path);
    case "neighborhood": return scope.nodeId;
    case "impact": return scope.target;
    case "flow": return flowRefLabel(scope.flow);
    case "group": return `${scope.nodeIds.length} nodes`;
  }
}

// Deep trails (e.g. tracing a flow across several services) grow one crumb
// per hop with no ceiling. Past COLLAPSE_THRESHOLD entries, everything
// between the root and the last TAIL_COUNT crumbs folds into a single "…"
// that opens a jump-to-any-hidden-crumb menu, so the trail's on-screen width
// stops growing once it's deep enough to matter.
const COLLAPSE_THRESHOLD = 5;
const TAIL_COUNT = 2;

type Entry = { kind: "crumb"; scope: Scope; index: number } | { kind: "collapsed"; hidden: Scope[]; fromIndex: number };

function buildEntries(stack: readonly Scope[]): Entry[] {
  if (stack.length <= COLLAPSE_THRESHOLD) {
    return stack.map((scope, index) => ({ kind: "crumb", scope, index }));
  }
  const tailStart = stack.length - TAIL_COUNT;
  const entries: Entry[] = [{ kind: "crumb", scope: stack[0], index: 0 }];
  entries.push({ kind: "collapsed", hidden: stack.slice(1, tailStart), fromIndex: 1 });
  for (let i = tailStart; i < stack.length; i++) entries.push({ kind: "crumb", scope: stack[i], index: i });
  return entries;
}

function CollapsedCrumb(props: { hidden: Scope[]; fromIndex: number }) {
  const [open, setOpen] = createSignal(false);
  return (
    <div class="relative shrink-0">
      <button
        data-testid="breadcrumb-collapsed"
        class="hover:text-white px-0.5"
        title={`${props.hidden.length} hidden`}
        onClick={() => setOpen((v) => !v)}
      >
        …
      </button>
      <Show when={open()}>
        <div class="absolute top-full left-0 mt-1 z-20 bg-neutral-900 border border-neutral-700 rounded shadow-lg text-xs whitespace-nowrap py-1">
          <For each={props.hidden}>
            {(scope, i) => (
              <button
                class="block w-full text-left px-3 py-1 text-neutral-300 hover:bg-neutral-800 hover:text-white truncate max-w-[280px]"
                title={crumbTitle(scope)}
                onClick={() => {
                  setOpen(false);
                  scopeStore.popTo(props.fromIndex + i());
                }}
              >
                {crumbLabel(scope)}
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}

export default function Breadcrumbs() {
  const entries = createMemo(() => buildEntries(scopeStore.stack()));

  return (
    <div class="flex items-center gap-1 text-sm text-neutral-400 min-w-0 flex-1">
      {/* A long navigation trail (e.g. clicking through several flows)
          scrolls within its own bounded strip instead of pushing the
          copy-context/reset controls — or the rest of the header (LensBar,
          pin tray) — out of view. */}
      <div class="flex items-center gap-1 min-w-0 overflow-x-auto whitespace-nowrap">
        <For each={entries()}>
          {(entry, i) => (
            <>
              {i() > 0 && <span class="text-neutral-500">▸</span>}
              {entry.kind === "crumb" ? (
                <button
                  class="hover:text-white truncate shrink-0"
                  title={crumbTitle(entry.scope)}
                  onClick={() => scopeStore.popTo(entry.index)}
                >
                  {crumbLabel(entry.scope)}
                </button>
              ) : (
                <CollapsedCrumb hidden={entry.hidden} fromIndex={entry.fromIndex} />
              )}
            </>
          )}
        </For>
      </div>
      <Show when={scopeStore.stack().at(-1)?.kind !== "search"}>
        <button
          data-testid="breadcrumb-copy-context"
          class="ml-1 text-blue-400 hover:text-blue-300 text-xs shrink-0"
          title="Copy context for current scope"
          onClick={() => {
            const top = scopeStore.stack().at(-1);
            if (top) contextCopyStore.copy(copySourceForScope(top));
          }}
        >
          ⧉
        </button>
      </Show>
      <button
        class="ml-1 text-neutral-500 hover:text-white text-xs shrink-0"
        onClick={() => scopeStore.reset()}
      >
        [×]
      </button>
    </div>
  );
}
