import { Component, Show } from "solid-js";
import { uiStore } from "../stores/ui";

const Notification: Component = () => {
  return (
    <Show when={uiStore.notification()}>
      {(msg) => (
        <div
          class="fixed bottom-4 right-4 bg-indigo-700 text-white text-sm rounded px-4 py-2 shadow-lg z-50"
          onClick={() => uiStore.clearNotification()}
        >
          {msg()}
        </div>
      )}
    </Show>
  );
};

export default Notification;
