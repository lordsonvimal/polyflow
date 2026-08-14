// UF.3: the detail-panel section for a selected edge — channel key,
// verification state, evidence sources, producer/consumer counts. Rendered
// for ANY edge (context-menu "Isolate seam" works on any edge kind); an
// edge kind /api/seam can't expand past its own two endpoints (`expanded:
// false`) renders the pair alone with an honest "no channel closure" note
// instead of pretending it found a real channel (rule 12).
import { createResource, For, Show } from "solid-js";
import { fetchSeam } from "../canvas/scopes/flow";
import { scopeStore } from "../../stores/scope";

export default function SeamSummary(props: { edgeId: string }) {
  const [resolution] = createResource(() => props.edgeId, (id) => fetchSeam(id));

  function isolate() {
    scopeStore.push({ kind: "flow", flow: { kind: "seam", edgeId: props.edgeId } });
  }

  return (
    <div data-testid="seam-summary" class="mt-2 border-t border-neutral-800 pt-2">
      <Show when={resolution.loading}>
        <div class="text-xs text-neutral-400">Loading seam…</div>
      </Show>
      <Show when={resolution.error}>
        <div class="text-xs text-neutral-400">Failed to load seam.</div>
      </Show>
      <Show when={resolution()}>
        {(seam) => (
          <div class="text-xs text-neutral-300 space-y-1">
            <div class="text-neutral-200 truncate" title={seam().channel}>
              {seam().channel}
            </div>
            <Show when={seam().verification_state}>
              <div class="text-neutral-400">verification: {seam().verification_state}</div>
            </Show>
            <div class="text-neutral-400">
              {seam().producers.length} producer{seam().producers.length === 1 ? "" : "s"} ·{" "}
              {seam().consumers.length} consumer{seam().consumers.length === 1 ? "" : "s"}
            </div>
            <Show when={seam().sources?.length}>
              <ul class="text-neutral-400">
                <For each={seam().sources}>
                  {(s) => <li>{s.provider} ({s.confidence})</li>}
                </For>
              </ul>
            </Show>
            <Show when={!seam().expanded}>
              <div data-testid="seam-summary-no-closure" class="text-neutral-500">
                no channel closure — this edge kind has nothing to expand past its own pair
              </div>
            </Show>
            <button
              data-testid="seam-summary-isolate"
              class="text-indigo-300 hover:text-indigo-200"
              onClick={isolate}
            >
              Isolate seam →
            </button>
          </div>
        )}
      </Show>
    </div>
  );
}
