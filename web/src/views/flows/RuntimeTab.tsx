import { For, Show, createEffect, createMemo, createSignal, onMount } from "solid-js";
import { captureStore, type CaptureSessionInfo } from "../../stores/capture";
import { runtimeStore, type ObservedOnlyGap } from "../../stores/runtime";
import { runtimeViewStore } from "../../stores/runtimeView";
import { drawerStore } from "../../stores/drawer";
import { notificationsStore } from "../../stores/notifications";
import { layoutPrefs } from "../../stores/layoutPrefs";

function allSessionNames(): string[] {
  const names = new Set<string>();
  captureStore.activeSessions().forEach((s) => names.add(s.session));
  captureStore.sessions().forEach((s) => names.add(s.Name));
  return [...names];
}

function sessionSpanCount(name: string): number {
  const active = captureStore.activeSessions().find((s) => s.session === name);
  if (active) return active.spans_received;
  const info = captureStore.sessions().find((s: CaptureSessionInfo) => s.Name === name);
  return info?.SpanCount ?? 0;
}

function ProposalRow(props: { gap: ObservedOnlyGap }) {
  const [open, setOpen] = createSignal(false);
  const key = () => runtimeStore.gapKey(props.gap);
  const proposal = () => runtimeStore.proposals()[key()];

  async function toggle(): Promise<void> {
    if (open()) {
      setOpen(false);
      return;
    }
    if (!proposal()) await runtimeStore.proposeRule(props.gap);
    setOpen(true);
  }

  async function copy(): Promise<void> {
    const p = proposal();
    if (!p) return;
    try {
      await navigator.clipboard?.writeText(p.content);
      notificationsStore.add({ id: `runtime-propose-copy-${Date.now()}`, kind: "success", message: "Proposal YAML copied" });
    } catch {
      notificationsStore.add({ id: `runtime-propose-copy-err-${Date.now()}`, kind: "error", message: "Could not copy" });
    }
  }

  return (
    <li data-testid="runtime-gap-row" class="border-b border-neutral-900 py-1">
      <div class="flex items-center gap-2 text-xs">
        <span class="text-amber-400">{props.gap.Kind}</span>
        <span class="text-neutral-300 truncate">{props.gap.Key}</span>
        <span class="text-neutral-500 truncate">{props.gap.From} → {props.gap.To || "?"}</span>
        <button data-testid="runtime-gap-propose" class="ml-auto text-indigo-300 hover:text-indigo-200" onClick={() => void toggle()}>
          {open() ? "hide" : "propose contract rule"}
        </button>
      </div>
      <Show when={open()}>
        <Show when={proposal()} fallback={<div class="text-neutral-500 text-xs mt-1">Generating…</div>}>
          {(p) => (
            <div class="mt-1 space-y-1">
              <pre data-testid="runtime-gap-proposal-yaml" class="whitespace-pre-wrap text-[10px] text-neutral-300 bg-neutral-900 rounded p-1.5 max-h-48 overflow-y-auto">
                {p().content}
              </pre>
              <button data-testid="runtime-gap-proposal-copy" class="text-xs text-neutral-400 hover:text-white" onClick={() => void copy()}>
                Copy YAML ({p().filename})
              </button>
            </div>
          )}
        </Show>
      </Show>
    </li>
  );
}

function CoveragePanel(props: { session: string }) {
  const cov = () => runtimeStore.coverage();
  return (
    <Show when={cov()}>
      {(c) => (
        <div data-testid="runtime-coverage" class="space-y-3">
          <div class="flex items-center gap-3 text-xs text-neutral-400">
            <span>verified <span class="text-emerald-400">{c().VerifiedChannels}</span></span>
            <span>observed-only <span class="text-amber-400">{c().GapChannels}</span></span>
            <span>static-only <span class="text-neutral-300">{c().CandidateChannels}</span></span>
            <button
              data-testid="runtime-coverage-health-link"
              title="This session's coverage only — see the Health dashboard for the graph-wide, all-session view"
              class="ml-auto text-indigo-300 hover:text-indigo-200"
              onClick={() => layoutPrefs.setActivity("health")}
            >
              cumulative view (Health) →
            </button>
          </div>
          <table class="w-full text-xs text-neutral-300">
            <thead>
              <tr class="text-neutral-500 text-left">
                <th class="font-normal">Kind</th>
                <th class="font-normal text-right">Verified</th>
                <th class="font-normal text-right">Candidate</th>
                <th class="font-normal text-right">Gap</th>
                <th class="font-normal text-right">%</th>
              </tr>
            </thead>
            <tbody>
              <For each={c().Rows}>
                {(row) => (
                  <tr data-testid="runtime-coverage-row">
                    <td>{row.Kind}</td>
                    <td class="text-right">{row.Verified}</td>
                    <td class="text-right">{row.Candidate}</td>
                    <td class="text-right">{row.Gap}</td>
                    <td class="text-right">{row.Pct.toFixed(0)}%</td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
          <Show when={c().ObservedOnlyGaps.length > 0}>
            <div>
              <div class="text-neutral-400 text-xs mb-1">Observed-only (no static edge)</div>
              <ul data-testid="runtime-gap-list">
                <For each={c().ObservedOnlyGaps}>{(gap) => <ProposalRow gap={gap} />}</For>
              </ul>
            </div>
          </Show>
        </div>
      )}
    </Show>
  );
}

function FlowsAndLedger() {
  return (
    <div class="space-y-3">
      <div>
        <div class="text-neutral-400 text-xs mb-1">Observed flows ({runtimeStore.flowRecords().length})</div>
        <table class="w-full text-xs text-neutral-300">
          <thead>
            <tr class="text-neutral-500 text-left">
              <th class="font-normal">Kind</th>
              <th class="font-normal">Channel</th>
              <th class="font-normal">From → To</th>
              <th class="font-normal">Causality</th>
            </tr>
          </thead>
          <tbody>
            <For each={runtimeStore.flowRecords()}>
              {(f) => (
                <tr data-testid="runtime-flow-row">
                  <td>{f.Kind}</td>
                  <td class="truncate max-w-[200px]">{f.Key}</td>
                  <td class="truncate max-w-[200px]">{f.FromService || "?"} → {f.ToService || "?"}</td>
                  <td class="text-neutral-500">{f.Causality}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
        <Show when={runtimeStore.flowRecords().length === 0}>
          <div class="text-neutral-500 text-xs">No flow records for this session.</div>
        </Show>
      </div>
      {/* Ingest ledger — never hidden, even when empty, per the plan. */}
      <div>
        <div class="text-neutral-400 text-xs mb-1">Ingest ledger — unmapped spans ({runtimeStore.ledger().length})</div>
        <Show when={runtimeStore.ledger().length > 0} fallback={<div class="text-neutral-500 text-xs">Every observed span mapped cleanly.</div>}>
          <ul data-testid="runtime-ledger-list">
            <For each={runtimeStore.ledger()}>
              {(l) => (
                <li data-testid="runtime-ledger-row" class="flex items-center gap-2 text-xs text-neutral-400 border-b border-neutral-900 py-0.5">
                  <span class="text-red-400">{l.Reason}</span>
                  <span class="truncate">{l.Service}</span>
                  <span class="text-neutral-600 ml-auto truncate">{l.TraceID}/{l.SpanID}</span>
                </li>
              )}
            </For>
          </ul>
        </Show>
      </div>
    </div>
  );
}

export default function RuntimeTab() {
  const [uploadError, setUploadError] = createSignal<string | null>(null);
  let fileInputRef: HTMLInputElement | undefined;

  const sessions = createMemo(() => allSessionNames().sort());

  onMount(() => {
    void captureStore.refreshStatus();
    if (!runtimeViewStore.selectedSession() && sessions().length > 0) {
      runtimeViewStore.setSelectedSession(sessions()[0]);
    }
  });

  createEffect(() => {
    const s = runtimeViewStore.selectedSession();
    if (s) void runtimeStore.load(s);
  });

  createEffect(() => {
    // Once sessions load asynchronously, default to the newest if nothing selected yet.
    if (!runtimeViewStore.selectedSession() && sessions().length > 0) {
      runtimeViewStore.setSelectedSession(sessions()[0]);
    }
  });

  async function onDumpSelected(e: Event): Promise<void> {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    setUploadError(null);
    try {
      await captureStore.ingestDump(file, runtimeViewStore.selectedSession() ?? undefined);
      const s = runtimeViewStore.selectedSession();
      if (s) void runtimeStore.load(s);
    } catch (err) {
      setUploadError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <div data-testid="runtime-tab" class="p-2 text-xs text-neutral-300 flex flex-col h-full gap-2 overflow-y-auto">
      <div class="flex items-center gap-2 shrink-0">
        <span class="text-neutral-400">Session</span>
        <select
          data-testid="runtime-session-select"
          class="bg-neutral-900 border border-neutral-700 rounded px-1.5 py-0.5 text-neutral-200"
          value={runtimeViewStore.selectedSession() ?? ""}
          onChange={(e) => runtimeViewStore.setSelectedSession(e.currentTarget.value || null)}
        >
          <For each={sessions()}>
            {(name) => (
              <option value={name}>
                {name} ({sessionSpanCount(name)} spans)
              </option>
            )}
          </For>
        </select>
        <Show when={sessions().length === 0}>
          <span class="text-neutral-500">No capture sessions yet — click ◉ Record to start one.</span>
        </Show>
        <button
          data-testid="runtime-import-dump"
          class="ml-auto text-neutral-400 hover:text-white border border-neutral-700 rounded px-2 py-0.5"
          onClick={() => fileInputRef?.click()}
        >
          Import OTLP dump…
        </button>
        <input ref={fileInputRef} type="file" class="hidden" onChange={(e) => void onDumpSelected(e)} />
        <button
          data-testid="runtime-jobs-link"
          class="text-neutral-400 hover:text-white"
          title="CLI-started captures and index runs both show up in the Jobs tab"
          onClick={() => drawerStore.openJobs()}
        >
          Jobs ↗
        </button>
      </div>
      <Show when={uploadError()}>
        <div data-testid="runtime-upload-error" class="text-red-400">{uploadError()}</div>
      </Show>

      <Show when={runtimeViewStore.selectedSession()} fallback={<div class="text-neutral-500">Select or start a capture session.</div>}>
        <Show when={runtimeStore.loading()}>
          <div class="text-neutral-400">Loading…</div>
        </Show>
        <Show when={runtimeStore.error()}>
          <div data-testid="runtime-error" class="text-red-400">{runtimeStore.error()}</div>
        </Show>
        <Show when={!runtimeStore.loading() && !runtimeStore.error()}>
          <CoveragePanel session={runtimeViewStore.selectedSession()!} />
          <FlowsAndLedger />
        </Show>
      </Show>
    </div>
  );
}
