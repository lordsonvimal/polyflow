import { For, Show, createEffect, createMemo, createSignal, onMount } from "solid-js";
import { jobsStore, type Job } from "../../stores/jobs";

const STATE_ICON: Record<Job["state"], string> = {
  running: "●",
  succeeded: "✓",
  failed: "✗",
  canceled: "⊘",
};

const STATE_COLOR: Record<Job["state"], string> = {
  running: "text-indigo-300",
  succeeded: "text-emerald-400",
  failed: "text-red-400",
  canceled: "text-neutral-400",
};

function formatDuration(startedAt: string, endedAt?: string): string {
  const start = Date.parse(startedAt);
  const end = endedAt ? Date.parse(endedAt) : Date.now();
  if (Number.isNaN(start) || Number.isNaN(end)) return "--";
  const ms = Math.max(0, end - start);
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m${Math.round(s % 60)}s`;
}

function RunningJobCard(props: { job: Job }) {
  const [paused, setPaused] = createSignal(false);
  const [confirmingCancel, setConfirmingCancel] = createSignal(false);
  let logRef: HTMLDivElement | undefined;

  createEffect(() => {
    props.job.log_tail; // eslint-disable-line @typescript-eslint/no-unused-expressions
    if (paused()) return;
    if (logRef) logRef.scrollTop = logRef.scrollHeight;
  });

  const pct = createMemo(() => {
    const { done, total } = props.job.progress;
    if (total <= 0) return null;
    return Math.min(100, Math.round((done / total) * 100));
  });

  return (
    <div data-testid="jobs-running-card" class="border border-neutral-800 rounded p-2 space-y-2">
      <div class="flex items-center gap-2">
        <span class={`${STATE_COLOR[props.job.state]}`}>{STATE_ICON[props.job.state]}</span>
        <span class="text-neutral-200 font-medium">{props.job.kind}</span>
        <span data-testid="jobs-running-progress" class="text-neutral-400 ml-auto">
          {props.job.progress.done}/{props.job.progress.total || "?"}
        </span>
      </div>
      <div class="h-1.5 rounded bg-neutral-800 overflow-hidden">
        <Show
          when={pct() !== null}
          fallback={<div class="h-full w-1/3 bg-indigo-500 animate-pulse" />}
        >
          <div class="h-full bg-indigo-500 transition-all" style={{ width: `${pct()}%` }} />
        </Show>
      </div>
      <div
        ref={logRef}
        data-testid="jobs-log-tail"
        class="h-24 overflow-y-auto bg-neutral-900 rounded p-1.5 font-mono text-[10px] text-neutral-400 whitespace-pre-wrap"
      >
        <For each={props.job.log_tail}>{(line) => <div>{line}</div>}</For>
      </div>
      <div class="flex items-center gap-2">
        <button
          data-testid="jobs-log-pause"
          class="text-neutral-400 hover:text-white"
          onClick={() => setPaused((p) => !p)}
        >
          {paused() ? "▶ resume autoscroll" : "⏸ pause autoscroll"}
        </button>
        <Show
          when={!confirmingCancel()}
          fallback={
            <span class="ml-auto flex items-center gap-1">
              <span class="text-neutral-400">Cancel this job?</span>
              <button
                data-testid="jobs-cancel-confirm"
                class="text-red-400 hover:text-red-300"
                onClick={() => {
                  setConfirmingCancel(false);
                  jobsStore.cancel(props.job.id);
                }}
              >
                Yes
              </button>
              <button class="text-neutral-400 hover:text-white" onClick={() => setConfirmingCancel(false)}>
                No
              </button>
            </span>
          }
        >
          <button
            data-testid="jobs-cancel"
            class="ml-auto text-neutral-400 hover:text-red-300"
            onClick={() => setConfirmingCancel(true)}
          >
            Cancel
          </button>
        </Show>
      </div>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function HistoryRow(props: { job: Job }) {
  const [expanded, setExpanded] = createSignal(false);
  const finished = () => props.job.state !== "running";
  return (
    <li data-testid="jobs-history-row" class="border-b border-neutral-900 py-1">
      <div class="flex items-center gap-2">
        <span class={STATE_COLOR[props.job.state]}>{STATE_ICON[props.job.state]}</span>
        <span class="text-neutral-300">{props.job.kind}</span>
        <span class="text-neutral-500">{props.job.started_at}</span>
        <span class="text-neutral-400 ml-auto">{formatDuration(props.job.started_at, props.job.ended_at)}</span>
        <Show when={props.job.state === "failed"}>
          <button
            data-testid="jobs-history-error-toggle"
            class="text-red-400 hover:text-red-300"
            onClick={() => setExpanded((v) => !v)}
          >
            {expanded() ? "hide" : "error"}
          </button>
        </Show>
      </div>
      <Show when={expanded() && props.job.error}>
        <pre data-testid="jobs-history-error" class="mt-1 whitespace-pre-wrap text-[10px] text-red-300 bg-neutral-900 rounded p-1.5">
          {props.job.error}
        </pre>
      </Show>
      <Show when={finished() && props.job.profile}>
        <div data-testid="jobs-history-profile" class="flex items-center gap-3 text-neutral-600 mt-0.5">
          <span title="heap in use at completion">alloc {formatBytes(props.job.profile.alloc_bytes)}</span>
          <span title="cumulative bytes allocated during this job">total alloc {formatBytes(props.job.profile.total_alloc_bytes)}</span>
          <span title="garbage-collector cycles run during this job">gc {props.job.profile.gc_count}</span>
          <Show when={props.job.profile.has_cpu_profile}>
            <a
              data-testid="jobs-history-profile-download"
              class="ml-auto text-indigo-300 hover:text-indigo-200 underline"
              href={`/api/jobs/${props.job.id}/profile`}
              download={`job-${props.job.id}.pprof`}
            >
              ↓ CPU profile
            </a>
          </Show>
        </div>
      </Show>
    </li>
  );
}

export default function JobsTab() {
  onMount(() => jobsStore.fetchHistory());

  return (
    <div data-testid="jobs-tab" class="p-2 text-xs text-neutral-300 flex flex-col h-full gap-2">
      <Show when={jobsStore.activeIndexJob()}>{(job) => <RunningJobCard job={job()} />}</Show>

      <div class="flex items-center shrink-0">
        <span class="text-neutral-400">History</span>
        <button data-testid="jobs-history-refresh" class="ml-auto text-neutral-400 hover:text-white" onClick={() => jobsStore.fetchHistory()}>
          ↻
        </button>
      </div>
      <div class="flex-1 overflow-y-auto min-h-0">
        <Show when={jobsStore.historyLoading()}>
          <div class="text-neutral-400">Loading…</div>
        </Show>
        <Show when={!jobsStore.historyLoading() && jobsStore.history().length === 0}>
          <div class="text-neutral-500">No jobs run yet.</div>
        </Show>
        <ul data-testid="jobs-history-list">
          <For each={jobsStore.history()}>{(job) => <HistoryRow job={job} />}</For>
        </ul>
      </div>
    </div>
  );
}
