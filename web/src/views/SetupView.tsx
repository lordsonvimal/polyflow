import { For, Show, createEffect, createMemo, createSignal, onMount } from "solid-js";
import { setupStore } from "../stores/setup";
import { jobsStore } from "../stores/jobs";

// UO.7 setup mode: shown by App.tsx in place of the normal shell when GET
// /api/setup/status reports needs_config or needs_index. Step 1 discovers
// services (jobs kind "init", workspace.Discover — no write) and shows them
// for confirmation before POST /api/setup/apply writes polyflow.yml
// (workspace.SaveInit, the exact function `polyflow init` calls). Step 2
// reuses the existing index-job flow (jobsStore.startIndex) so this wizard
// and `polyflow index` are the same code path. Step 3 just waits for GET
// /api/setup/status to report ready — the fsnotify watcher on graph.db
// (already wired for the normal reload flow) is what flips it.
type Step = "discover" | "confirm" | "index" | "done";

export default function SetupView() {
  const [step, setStep] = createSignal<Step>("discover");
  const [root, setRoot] = createSignal(".");
  const [indexStarted, setIndexStarted] = createSignal(false);

  onMount(() => {
    const s = setupStore.status();
    if (s && !s.needs_config) {
      setStep("index");
    }
  });

  async function runDiscover() {
    await setupStore.discover(root());
    if (setupStore.discovered()) setStep("confirm");
  }

  async function confirmAndApply() {
    const cfg = setupStore.discovered();
    if (!cfg) return;
    const ok = await setupStore.apply(cfg);
    if (ok) setStep("index");
  }

  async function startIndexing() {
    setIndexStarted(true);
    await jobsStore.startIndex(false);
  }

  const indexJob = createMemo(() => jobsStore.activeIndexJob());

  // Once indexing finishes, re-check setup status; a "ready" result lets
  // App.tsx swap back to the normal shell on its own next poll.
  const indexDone = createMemo(() => indexStarted() && !indexJob());

  createEffect(() => {
    if (indexDone()) setupStore.checkStatus();
  });

  return (
    <div data-testid="setup-view" class="flex flex-col items-center justify-center h-screen w-screen bg-neutral-950 text-neutral-100 p-6">
      <div class="max-w-xl w-full space-y-6">
        <div>
          <div class="text-lg font-semibold text-white">Welcome to polyflow</div>
          <div class="text-sm text-neutral-400">No workspace found yet — let's set one up.</div>
        </div>

        <Show when={step() === "discover"}>
          <div data-testid="setup-step-discover" class="space-y-3">
            <div class="text-sm text-neutral-300">Step 1 — discover services</div>
            <input
              data-testid="setup-root-input"
              class="w-full bg-neutral-900 border border-neutral-800 rounded px-2 py-1 text-sm"
              value={root()}
              onInput={(e) => setRoot(e.currentTarget.value)}
              placeholder="workspace root (e.g. .)"
            />
            <Show when={setupStore.discoverError()}>
              <div data-testid="setup-discover-error" class="text-red-400 text-xs">{setupStore.discoverError()}</div>
            </Show>
            <button
              data-testid="setup-discover-button"
              class="px-3 py-1.5 rounded bg-indigo-700 hover:bg-indigo-600 disabled:opacity-50 text-white text-sm"
              disabled={setupStore.discovering()}
              onClick={runDiscover}
            >
              {setupStore.discovering() ? "Discovering…" : "Discover services"}
            </button>
          </div>
        </Show>

        <Show when={step() === "confirm"}>
          <div data-testid="setup-step-confirm" class="space-y-3">
            <div class="text-sm text-neutral-300">
              Discovered {setupStore.discovered()?.Services.length ?? 0} service(s):
            </div>
            <ul class="text-xs text-neutral-400 space-y-1 max-h-48 overflow-y-auto border border-neutral-800 rounded p-2">
              <For each={setupStore.discovered()?.Services ?? []}>
                {(svc) => (
                  <li data-testid="setup-discovered-service">
                    <span class="text-neutral-200 font-mono">{svc.Name}</span> — {svc.Path} ({svc.Language})
                  </li>
                )}
              </For>
            </ul>
            <Show when={setupStore.applyError()}>
              <div data-testid="setup-apply-error" class="text-red-400 text-xs">{setupStore.applyError()}</div>
            </Show>
            <div class="flex gap-2">
              <button
                class="px-3 py-1.5 rounded text-neutral-400 hover:text-white text-sm"
                onClick={() => setStep("discover")}
              >
                Back
              </button>
              <button
                data-testid="setup-confirm-button"
                class="px-3 py-1.5 rounded bg-indigo-700 hover:bg-indigo-600 disabled:opacity-50 text-white text-sm"
                disabled={setupStore.applying()}
                onClick={confirmAndApply}
              >
                {setupStore.applying() ? "Writing polyflow.yml…" : "Confirm & write polyflow.yml"}
              </button>
            </div>
          </div>
        </Show>

        <Show when={step() === "index"}>
          <div data-testid="setup-step-index" class="space-y-3">
            <div class="text-sm text-neutral-300">Step 2 — first index</div>
            <Show when={!indexJob()}>
              <button
                data-testid="setup-index-button"
                class="px-3 py-1.5 rounded bg-indigo-700 hover:bg-indigo-600 text-white text-sm"
                onClick={startIndexing}
              >
                Start indexing
              </button>
            </Show>
            <Show when={indexJob()}>
              <div data-testid="setup-index-progress" class="text-xs text-neutral-400">
                Indexing… {indexJob()?.progress.done ?? 0}/{indexJob()?.progress.total ?? 0}
              </div>
            </Show>
            <Show when={indexDone()}>
              <div data-testid="setup-index-done" class="text-xs text-emerald-400">
                Index complete — loading the overview…
              </div>
            </Show>
          </div>
        </Show>
      </div>
    </div>
  );
}
