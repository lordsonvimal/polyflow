// UO.3: Health & trust dashboard — a canvas-free page rendering GET
// /api/health (UB.5's backend). Extra scope: the tool's own trust numbers
// (doctor, eval, ledger, coverage) belong in the UI, not just `polyflow
// doctor` output.
import { For, Show, createMemo, onMount } from "solid-js";
import { healthStore, type HealthCoverage } from "../../stores/health";
import { drawerStore } from "../../stores/drawer";
import { runtimeViewStore } from "../../stores/runtimeView";

const COVERAGE_ROWS: { key: keyof HealthCoverage; label: string; explanation: string; color: string }[] = [
  {
    key: "verified",
    label: "verified",
    explanation: "confirmed by runtime traces or contract evidence",
    color: "bg-emerald-500",
  },
  {
    key: "candidate",
    label: "candidate",
    explanation: "static edge not yet confirmed by runtime/contract evidence",
    color: "bg-amber-500",
  },
  {
    key: "observed_only_gap",
    label: "observed_only_gap",
    explanation: "seen at runtime with no matching static edge — traced but not statically resolved",
    color: "bg-indigo-400",
  },
  {
    key: "conflicting",
    label: "conflicting",
    explanation: "static and runtime evidence disagree",
    color: "bg-red-500",
  },
];

function Card(props: { title: string; testId: string; children: any }) {
  return (
    <div data-testid={props.testId} class="border border-neutral-800 rounded p-3 space-y-2">
      <div class="text-xs font-semibold text-neutral-200">{props.title}</div>
      {props.children}
    </div>
  );
}

function IndexCard() {
  const idx = () => healthStore.data()!.index;
  return (
    <Card title="Index" testId="health-index-card">
      <div class="grid grid-cols-2 gap-1 text-xs text-neutral-300">
        <div class="text-neutral-500">Indexed at</div>
        <div data-testid="health-indexed-at">{idx().indexed_at || "never"}</div>
        <div class="text-neutral-500">Schema version</div>
        <div>{idx().schema_version || "--"}</div>
        <div class="text-neutral-500">Nodes</div>
        <div>{idx().nodes}</div>
        <div class="text-neutral-500">Edges</div>
        <div>{idx().edges}</div>
        <div class="text-neutral-500">Parse errors</div>
        <div>
          <button
            data-testid="health-parse-errors-count"
            class={idx().parse_errors > 0 ? "text-amber-400 hover:underline" : "text-neutral-300"}
            disabled={idx().parse_errors === 0}
            onClick={() => document.getElementById("health-parse-error-list")?.scrollIntoView({ block: "nearest" })}
          >
            {idx().parse_errors}
          </button>
        </div>
      </div>
      <Show when={idx().parse_errors > 0}>
        <details id="health-parse-error-list" data-testid="health-parse-error-list">
          <summary class="text-xs text-neutral-400 cursor-pointer">Parse error files</summary>
          <ul class="mt-1 space-y-0.5 max-h-40 overflow-y-auto">
            <For each={idx().parse_error_list}>
              {(pe) => (
                <li data-testid="health-parse-error-row" class="text-xs text-neutral-400 flex gap-2">
                  <span class="text-neutral-500 shrink-0">{pe.service}</span>
                  <span class="truncate">
                    {pe.file_path}:{pe.first_error_line}
                  </span>
                  <span class="ml-auto shrink-0">{pe.error_count} error{pe.error_count === 1 ? "" : "s"}</span>
                </li>
              )}
            </For>
          </ul>
        </details>
      </Show>
    </Card>
  );
}

function CoverageCard() {
  const cov = () => healthStore.data()!.coverage;
  const total = createMemo(() => {
    const c = cov();
    return c.verified + c.candidate + c.observed_only_gap + c.conflicting;
  });
  return (
    <Card title="Coverage" testId="health-coverage-card">
      <div class="space-y-1.5">
        <button
          data-testid="health-coverage-runtime-link"
          class="text-xs text-indigo-300 hover:text-indigo-200"
          onClick={() => runtimeViewStore.openRuntime()}
        >
          per-session breakdown (Runtime tab) →
        </button>
        <For each={COVERAGE_ROWS}>
          {(row) => {
            const count = () => (cov()[row.key] as number) ?? 0;
            const pct = () => (total() > 0 ? Math.round((count() / total()) * 100) : 0);
            return (
              <div data-testid={`health-coverage-row-${row.label}`} class="text-xs">
                <div class="flex items-center justify-between text-neutral-300">
                  <span>{row.label}</span>
                  <span>{count()}</span>
                </div>
                <div class="h-1.5 bg-neutral-800 rounded overflow-hidden">
                  <div class={`h-full ${row.color}`} style={{ width: `${pct()}%` }} />
                </div>
                <div class="text-neutral-500 mt-0.5">{row.explanation}</div>
              </div>
            );
          }}
        </For>
      </div>
    </Card>
  );
}

function UnresolvedCard() {
  const total = () => healthStore.data()!.unresolved_total;
  const byKind = () => healthStore.data()!.unresolved_by_kind ?? {};
  const kinds = createMemo(() => Object.entries(byKind()).sort((a, b) => b[1] - a[1]));
  return (
    <Card title="Unresolved" testId="health-unresolved-card">
      <div class="text-xs text-neutral-300">
        {total()} unresolved ref{total() === 1 ? "" : "s"}
      </div>
      <ul class="space-y-0.5">
        <For each={kinds()}>
          {([kind, count]) => (
            <li
              data-testid="health-unresolved-kind-row"
              class="flex items-center justify-between text-xs text-neutral-300 hover:text-white cursor-pointer"
              onClick={() => drawerStore.openUnresolvedByKind(kind)}
            >
              <span>{kind}</span>
              <span class="text-neutral-500">{count}</span>
            </li>
          )}
        </For>
      </ul>
    </Card>
  );
}

function EvalCard() {
  const ev = () => healthStore.data()!.eval;
  return (
    <Card title="Eval" testId="health-eval-card">
      <Show
        when={ev().present}
        fallback={
          <div data-testid="health-eval-empty" class="text-xs text-neutral-500">
            no eval baseline found — run <code class="text-neutral-400">polyflow eval</code>
          </div>
        }
      >
        <table class="w-full text-xs text-neutral-300">
          <thead>
            <tr class="text-neutral-500 text-left">
              <th class="font-normal">Repo</th>
              <th class="font-normal text-right">Recall</th>
            </tr>
          </thead>
          <tbody>
            <For each={ev().repos ?? []}>
              {(repo) => (
                <tr data-testid="health-eval-row">
                  <td>{repo.name}</td>
                  <td class="text-right">{(repo.recall * 100).toFixed(1)}%</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </Show>
    </Card>
  );
}

export default function HealthPanel() {
  onMount(() => {
    void healthStore.load();
  });

  return (
    <div data-testid="health-panel" class="p-3 overflow-y-auto h-full">
      <Show when={healthStore.loading() && !healthStore.data()}>
        <div class="text-xs text-neutral-400">Loading…</div>
      </Show>
      <Show when={healthStore.error()}>
        <div data-testid="health-error" class="text-xs text-red-400">
          {healthStore.error()}
        </div>
      </Show>
      <Show when={healthStore.data()}>
        <div class="grid grid-cols-2 gap-3">
          <IndexCard />
          <CoverageCard />
          <UnresolvedCard />
          <EvalCard />
        </div>
      </Show>
    </div>
  );
}
