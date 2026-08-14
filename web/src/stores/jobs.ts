// UO.0: Jobs UI — starts/tracks the "index" job from the browser (UB.3's
// engine on the server side), mirrored live via SSE job_progress/job_done
// events (connectionStore) rather than polling.
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";
import { connectionStore } from "./connection";
import { notificationsStore } from "./notifications";
import { drawerStore } from "./drawer";
import { scopeStore } from "./scope";

export interface JobProgress {
  done: number;
  total: number;
}

export interface Job {
  id: string;
  kind: string;
  args: string;
  state: "running" | "succeeded" | "failed" | "canceled";
  started_at: string;
  ended_at?: string;
  progress: JobProgress;
  error?: string;
  result?: string;
  log_tail: string[];
}

// The single running "index" job the top-bar button reflects — jobs of
// other kinds (eval/reconcile, UB.3) exist in the history list but have no
// dedicated top-bar affordance yet.
const [activeIndexJob, setActiveIndexJob] = createSignal<Job | null>(null);
const [history, setHistory] = createSignal<Job[]>([]);
const [historyLoading, setHistoryLoading] = createSignal(false);
// "Graph updated — Reload view" banner, shown after a successful index job.
const [reloadBanner, setReloadBanner] = createSignal(false);

function parseErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    try {
      const body = JSON.parse(err.body) as { error?: string };
      return body.error || err.body || err.message;
    } catch {
      return err.body || err.message;
    }
  }
  return err instanceof Error ? err.message : String(err);
}

function upsertHistory(job: Job): void {
  setHistory((h) => [job, ...h.filter((j) => j.id !== job.id)]);
}

async function startIndex(full: boolean): Promise<void> {
  try {
    const res = await apiFetch("/api/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind: "index", args: { full } }),
      silent: true,
    });
    const data = (await res.json()) as { job: Job };
    setActiveIndexJob(data.job);
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      notificationsStore.add({ id: `job-conflict-${Date.now()}`, kind: "info", message: "Index already running" });
      try {
        const body = JSON.parse(err.body) as { job?: Job };
        if (body.job && body.job.kind === "index") setActiveIndexJob(body.job);
      } catch {
        // malformed 409 body — the toast + tab-open still stand
      }
      drawerStore.openJobs();
      return;
    }
    notificationsStore.add({
      id: `job-start-err-${Date.now()}`,
      kind: "error",
      message: "Failed to start index",
      detail: parseErrorMessage(err),
      action: { label: "open Jobs tab", onClick: drawerStore.openJobs },
    });
  }
}

async function cancel(id: string): Promise<void> {
  try {
    await apiFetch(`/api/jobs/${id}`, { method: "DELETE", silent: true });
  } catch (err) {
    notificationsStore.add({
      id: `job-cancel-err-${Date.now()}`,
      kind: "error",
      message: "Failed to cancel job",
      detail: parseErrorMessage(err),
    });
  }
}

async function fetchHistory(limit = 50): Promise<void> {
  setHistoryLoading(true);
  try {
    const data = await apiFetchJSON<{ jobs: Job[] }>(`/api/jobs?limit=${limit}`, { silent: true });
    setHistory(data.jobs);
  } catch (err) {
    notificationsStore.add({
      id: `job-history-err-${Date.now()}`,
      kind: "error",
      message: "Failed to load job history",
      detail: parseErrorMessage(err),
    });
  } finally {
    setHistoryLoading(false);
  }
}

function dismissReloadBanner(): void {
  setReloadBanner(false);
}

// The banner's "Reload view" action: re-resolves the current scope in place
// (CanvasHost watches scopeStore.reloadNonce) rather than a hard reset.
function reloadView(): void {
  setReloadBanner(false);
  scopeStore.requestReload();
}

function handleJobEvent(job: Job, done: boolean): void {
  if (job.kind === "index") {
    setActiveIndexJob(job.state === "running" ? job : null);
  }
  if (!done) return;
  upsertHistory(job);
  if (job.kind !== "index") return;
  if (job.state === "succeeded") {
    notificationsStore.add({ id: `job-done-${job.id}`, kind: "success", message: "Index complete" });
    setReloadBanner(true);
  } else if (job.state === "failed") {
    notificationsStore.add({
      id: `job-fail-${job.id}`,
      kind: "error",
      message: "Index failed: " + (job.error || "unknown error"),
      detail: job.error,
      action: { label: "open Jobs tab", onClick: drawerStore.openJobs },
    });
  }
}

// Wired once at module load — connectionStore.onEvent is nil-safe before
// connectionStore.start() (App.tsx's onMount) and simply queues no events
// until the SSE connection opens.
connectionStore.onEvent((ev) => {
  if (ev.type !== "job_progress" && ev.type !== "job_done") return;
  const job = (ev as { job?: Job }).job;
  if (!job) return;
  handleJobEvent(job, ev.type === "job_done");
});

export const jobsStore = {
  activeIndexJob,
  history,
  historyLoading,
  reloadBanner,
  startIndex,
  cancel,
  fetchHistory,
  reloadView,
  dismissReloadBanner,
  // Test-only: this store is a module-level singleton (like scopeStore,
  // pinboardStore, …), so tests that drive startIndex/SSE events need a way
  // to clear state between cases.
  reset: () => {
    setActiveIndexJob(null);
    setHistory([]);
    setHistoryLoading(false);
    setReloadBanner(false);
  },
};
