import { For, Show } from "solid-js";
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

export default function Breadcrumbs() {
  return (
    <div class="flex items-center gap-1 text-sm text-neutral-400 min-w-0 overflow-hidden">
      <For each={scopeStore.stack()}>
        {(scope, i) => (
          <>
            {i() > 0 && <span class="text-neutral-500">▸</span>}
            <button
              class="hover:text-white truncate"
              title={crumbTitle(scope)}
              onClick={() => scopeStore.popTo(i())}
            >
              {crumbLabel(scope)}
            </button>
          </>
        )}
      </For>
      <Show when={scopeStore.stack().at(-1)?.kind !== "search"}>
        <button
          data-testid="breadcrumb-copy-context"
          class="ml-1 text-blue-400 hover:text-blue-300 text-xs"
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
        class="ml-1 text-neutral-500 hover:text-white text-xs"
        onClick={() => scopeStore.reset()}
      >
        [×]
      </button>
    </div>
  );
}
