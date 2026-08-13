import { For } from "solid-js";
import { scopeStore } from "../stores/scope";

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
              {i() === 0 ? "◆ polyflow" : scope.kind}
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
