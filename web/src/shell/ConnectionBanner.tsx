import { Show } from "solid-js";
import { connectionStore } from "../stores/connection";

export default function ConnectionBanner() {
  return (
    <Show when={connectionStore.state() === "disconnected"}>
      <div
        data-testid="connection-banner"
        class="flex items-center gap-2 px-3 py-1 bg-red-900/60 text-red-200 text-xs shrink-0"
      >
        <span>Lost connection to polyflow serve — retrying in {connectionStore.retryIn()}s…</span>
        <button class="ml-auto hover:text-white underline" onClick={() => connectionStore.reconnectNow()}>
          Retry now
        </button>
      </div>
    </Show>
  );
}
