// UO.7 setup mode: GET /api/setup/status gates the App shell; the wizard
// itself drives a jobs.Manager "init" job (workspace.Discover, no write)
// for step 1, POST /api/setup/apply to write polyflow.yml
// (workspace.SaveInit — byte-identical to `polyflow init`), then the
// existing jobsStore.startIndex for step 2. Step 3 is just landing on the
// normal shell once GET /api/setup/status reports ready.
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";

export interface SetupStatus {
  needs_config: boolean;
  needs_index: boolean;
  config_path: string;
  db_path: string;
}

export interface DiscoveredService {
  Name: string;
  Path: string;
  Language: string;
  Frameworks?: string[];
  Port?: number;
}

export interface DiscoveredConfig {
  Name: string;
  Version: string;
  Services: DiscoveredService[];
  Links?: unknown[];
  Index?: { Exclude: string[] };
  Settings?: Record<string, unknown>;
}

const [status, setStatus] = createSignal<SetupStatus | null>(null);
// Defaults false (not true) so the normal shell renders immediately on
// first paint — matching every pre-UO.7 test/usage — and only swaps to the
// setup wizard once GET /api/setup/status actually confirms it's needed.
// A real "no workspace yet" boot briefly shows the (empty) shell before
// this flips, which is an acceptable trade against blocking first paint on
// a network round trip.
const [checking, setChecking] = createSignal(false);
const [discovering, setDiscovering] = createSignal(false);
const [discoverError, setDiscoverError] = createSignal<string | null>(null);
const [discovered, setDiscovered] = createSignal<DiscoveredConfig | null>(null);
const [applying, setApplying] = createSignal(false);
const [applyError, setApplyError] = createSignal<string | null>(null);

function errMessage(err: unknown): string {
  if (err instanceof ApiError) return err.body || err.message;
  return err instanceof Error ? err.message : String(err);
}

async function checkStatus(): Promise<SetupStatus | null> {
  setChecking(true);
  try {
    const s = await apiFetchJSON<SetupStatus>("/api/setup/status", { silent: true });
    setStatus(s);
    return s;
  } catch {
    // Setup status itself is unreachable — treat as "not in setup mode" so
    // a transient network blip doesn't strand the user on a wizard screen.
    setStatus(null);
    return null;
  } finally {
    setChecking(false);
  }
}

async function discover(root = "."): Promise<void> {
  setDiscovering(true);
  setDiscoverError(null);
  try {
    const res = await apiFetch("/api/jobs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind: "init", args: { root } }),
      silent: true,
    });
    const { job } = (await res.json()) as { job: { id: string } };
    const final = await pollJob(job.id);
    if (final.state === "failed") {
      setDiscoverError(final.error || "discovery failed");
      return;
    }
    setDiscovered(JSON.parse(final.result || "{}") as DiscoveredConfig);
  } catch (err) {
    setDiscoverError(errMessage(err));
  } finally {
    setDiscovering(false);
  }
}

interface JobPoll {
  id: string;
  state: "running" | "succeeded" | "failed" | "canceled";
  result?: string;
  error?: string;
}

async function pollJob(id: string): Promise<JobPoll> {
  for (;;) {
    const job = await apiFetchJSON<JobPoll>(`/api/jobs/${id}`, { silent: true });
    if (job.state !== "running") return job;
    await new Promise((r) => setTimeout(r, 250));
  }
}

async function apply(cfg: DiscoveredConfig): Promise<boolean> {
  setApplying(true);
  setApplyError(null);
  try {
    await apiFetch("/api/setup/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
      silent: true,
    });
    await checkStatus();
    return true;
  } catch (err) {
    setApplyError(errMessage(err));
    return false;
  } finally {
    setApplying(false);
  }
}

export const setupStore = {
  status,
  checking,
  discovering,
  discoverError,
  discovered,
  applying,
  applyError,
  checkStatus,
  discover,
  apply,
  pollJob,
  reset: () => {
    setStatus(null);
    setChecking(true);
    setDiscovering(false);
    setDiscoverError(null);
    setDiscovered(null);
    setApplying(false);
    setApplyError(null);
  },
};
