import { For } from "solid-js";
import { scopeStore, type Scope } from "../stores/scope";
import { flowRefLabel } from "../views/canvas/scopes/flow";

function crumbLabel(scope: Scope): string {
  switch (scope.kind) {
    case "overview": return "overview";
    case "search": return "search";
    case "service": return scope.service;
    case "folder": return scope.path;
    case "file": return scope.path;
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
            {i() > 0 && <span class="text-neutral-600">▸</span>}
            <button
              class="hover:text-white truncate"
              onClick={() => scopeStore.popTo(i())}
            >
              {crumbLabel(scope)}
            </button>
          </>
        )}
      </For>
      <button
        class="ml-1 text-neutral-600 hover:text-white text-xs"
        onClick={() => scopeStore.reset()}
      >
        [×]
      </button>
    </div>
  );
}
