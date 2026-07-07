import { Component, Show, onMount, onCleanup } from "solid-js";
import { uiStore } from "../stores/ui";

const Notification: Component = () => {
  let es: EventSource | undefined;

  onMount(() => {
    es = new EventSource("/api/events");
    es.onmessage = (evt) => {
      try {
        const data = JSON.parse(evt.data);
        if (data.type === "graph_updated") {
          uiStore.setNotification("Graph updated");
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
  );
};

export default Notification;
