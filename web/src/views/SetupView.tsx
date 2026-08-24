import { For, Show, createEffect, createMemo, createSignal, onMount } from "solid-js";
import { setupStore } from "../stores/setup";
import { jobsStore } from "../stores/jobs";
import AgentSetupPanel from "./setup/AgentSetupPanel";

// UO.7 setup mode: shown by App.tsx in place of the normal shell when GET
// /api/setup/status reports needs_config or needs_index. Step 1 discovers
// services (jobs kind "init", workspace.Discover — no write) and shows them
// for confirmation before POST /api/setup/apply writes polyflow.yml
// (workspace.SaveInit, the exact function `polyflow init` calls). Step 2
// reuses the existing index-job flow (jobsStore.startIndex) so this wizard
// and `polyflow index` are the same code path. Step 3 offers the same
// agent-registration wizard `polyflow setup --agent <name>` runs on the
// CLI — optional, so it has its own "Finish setup" exit rather than
// auto-advancing like the earlier steps. That's what defers the GET
// /api/setup/status recheck: it only fires once the user leaves step 3,
// not the moment indexing finishes, otherwise App.tsx would swap back to
// the normal shell before the agent step ever rendered.
type Step = "discover" | "confirm" | "index" | "agent" | "done";

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

  // Once indexing finishes, move to the (optional) agent-registration step
  // rather than immediately rechecking setup status — that recheck is what
  // lets App.tsx swap back to the normal shell, so firing it here would
  // skip step 3 entirely.
  const indexDone = createMemo(() => indexStarted() && !indexJob());

  createEffect(() => {
    if (indexDone() && step() === "index") setStep("agent");
  });

  function finishSetup(): void {
    setStep("done");
    setupStore.checkStatus();
  }

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
              Discovered {setupStore.discovered()?.services?.length ?? 0} service(s):
            </div>
            <ul class="text-xs text-neutral-400 space-y-1 max-h-48 overflow-y-auto border border-neutral-800 rounded p-2">
              <For each={setupStore.discovered()?.services ?? []}>
                {(svc) => (
                  <li data-testid="setup-discovered-service">
                    <span class="text-neutral-200 font-mono">{svc.name}</span> — {svc.path} ({svc.language})
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
          </div>
        </Show>

        <Show when={step() === "agent"}>
          <div data-testid="setup-step-agent" class="space-y-3">
            <div class="text-sm text-neutral-300">Step 3 — connect a coding agent (optional)</div>
            <div class="text-xs text-neutral-500">
              Register polyflow's MCP server with a coding agent now, or skip and do it later from Settings → Agents
              (same as running <code class="text-neutral-400">polyflow setup</code> in a terminal).
            </div>
            <AgentSetupPanel />
            <button
              data-testid="setup-agent-finish-button"
              class="px-3 py-1.5 rounded bg-indigo-700 hover:bg-indigo-600 text-white text-sm"
              onClick={finishSetup}
            >
              Finish setup
            </button>
          </div>
        </Show>

        <Show when={step() === "done"}>
          <div data-testid="setup-step-done" class="text-xs text-emerald-400">
            Setup complete — loading the overview…
          </div>
        </Show>
      </div>
    </div>
  );
}
