import { Component, For, Show, onMount, onCleanup } from "solid-js";
import { uiStore } from "../stores/ui";

async function fetchWarnings() {
  try {
    const res = await fetch("/api/stats");
    if (!res.ok) return;
    const data = await res.json();
    const warnings: string[] = data.semantic_warnings ?? [];
    uiStore.setSemanticWarnings(warnings);
  } catch {
    // ignore fetch errors
  }
}

const Notification: Component = () => {
  let es: EventSource | undefined;

  onMount(() => {
    fetchWarnings();

    es = new EventSource("/api/events");
    es.onmessage = (evt) => {
      try {
        const data = JSON.parse(evt.data);
        if (data.type === "graph_updated") {
          uiStore.setNotification("Graph updated");
          fetchWarnings();
        }
      } catch {
        // ignore malformed events
      }
    };
  });

  onCleanup(() => {
    es?.close();
  });

  return (
    <>
      {/* Semantic fallback warning banner */}
      <Show when={uiStore.semanticWarnings().length > 0}>
        <div class="fixed top-0 left-0 right-0 z-50 bg-yellow-900 border-b border-yellow-600 text-yellow-200 text-xs px-4 py-2">
          <div class="flex items-start justify-between gap-4 max-w-screen-xl mx-auto">
            <div class="flex flex-col gap-1">
              <span class="font-semibold">
                ⚠ Semantic analysis unavailable for some services — call edges may be incomplete (~80% accuracy)
              </span>
              <For each={uiStore.semanticWarnings()}>
                {(w) => <span class="opacity-80">{w}</span>}
              </For>
            </div>
            <button
              class="shrink-0 text-yellow-400 hover:text-yellow-100 cursor-pointer"
              onClick={() => uiStore.clearSemanticWarnings()}
            >
              ✕
            </button>
          </div>
        </div>
      </Show>

      {/* Graph-updated toast */}
      <Show when={uiStore.notification()}>
        {(msg) => (
          <div
            class="fixed bottom-4 right-4 bg-indigo-700 text-white text-sm rounded px-4 py-2 shadow-lg z-50 cursor-pointer"
            onClick={() => uiStore.clearNotification()}
          >
            {msg()}
          </div>
        )}
      </Show>
    </>
  );
};

export default Notification;
