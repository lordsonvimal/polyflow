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
  name: string;
  path: string;
  language: string;
  frameworks?: string[];
  port?: number;
}

export interface DiscoveredConfig {
  name: string;
  version: string;
  services: DiscoveredService[];
  links?: unknown[];
  index?: { exclude: string[] };
  settings?: Record<string, unknown>;
}

export interface RegistryEntry {
  service: string;
  local_path: string;
  indexed_at?: string;
  fleets?: string[];
}

export type SetupScope = "repo" | "user" | "global";

export interface SetupAgentInfo {
  name: string;
  display_name: string;
  description: string;
  supports_hooks: boolean;
  supports_global_scope: boolean;
  mcp_configured: boolean;
  mcp_status_error?: string;
  hooks_configured: boolean;
  hooks_status_error?: string;
  supports_nudge: boolean;
  nudge_configured: boolean;
  nudge_status_error?: string;
}

export interface SetupAgentApplyResult {
  mcp_result: string;
  hooks_result?: string;
  hooks_skipped?: string;
  nudge_result?: string;
  nudge_skipped?: string;
}

export interface SetupAgentRemoveResult {
  mcp_result: string;
  hooks_result?: string;
  nudge_result?: string;
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

const [registryEntries, setRegistryEntries] = createSignal<RegistryEntry[]>([]);
const [registryLoading, setRegistryLoading] = createSignal(false);
const [registryError, setRegistryError] = createSignal<string | null>(null);
const [selecting, setSelecting] = createSignal<string | null>(null);
const [selectError, setSelectError] = createSignal<string | null>(null);

const [agentScope, setAgentScope] = createSignal<SetupScope>("repo");
const [agents, setAgents] = createSignal<SetupAgentInfo[]>([]);
const [agentsLoading, setAgentsLoading] = createSignal(false);
const [agentsError, setAgentsError] = createSignal<string | null>(null);
const [applyingAgent, setApplyingAgent] = createSignal<string | null>(null);
const [agentApplyResults, setAgentApplyResults] = createSignal<Record<string, SetupAgentApplyResult>>({});
const [agentApplyErrors, setAgentApplyErrors] = createSignal<Record<string, string>>({});
const [removingAgent, setRemovingAgent] = createSignal<string | null>(null);
const [agentRemoveResults, setAgentRemoveResults] = createSignal<Record<string, SetupAgentRemoveResult>>({});
const [agentRemoveErrors, setAgentRemoveErrors] = createSignal<Record<string, string>>({});

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

// loadRegistry fetches this machine's known local workspaces (GET
// /api/setup/registry) so the discover step can offer "open one of these"
// instead of always requiring a fresh filesystem walk — most useful when
// `polyflow serve` starts outside any of them (e.g. a parent directory of
// several independently fleet-configured repos), since a plain path-based
// discover from there finds nothing.
async function loadRegistry(): Promise<void> {
  setRegistryLoading(true);
  setRegistryError(null);
  try {
    const res = await apiFetchJSON<{ entries: RegistryEntry[] }>("/api/setup/registry", { silent: true });
    setRegistryEntries(res.entries);
  } catch (err) {
    setRegistryError(errMessage(err));
  } finally {
    setRegistryLoading(false);
  }
}

// selectWorkspace hands a known registry path to POST /api/setup/select,
// which restarts the server process pointed at it (SelectWorkspaceFunc,
// UO.8) on the same host:port. This browser tab's server connection drops
// for a moment as the old process exits and the new one comes up — poll
// GET /api/setup/status until it answers again, then reload so the whole
// app re-mounts against the new workspace from scratch rather than trying
// to patch every store in place.
async function selectWorkspace(localPath: string): Promise<void> {
  setSelecting(localPath);
  setSelectError(null);
  try {
    await apiFetch("/api/setup/select", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: localPath }),
      silent: true,
    });
    await waitForRestart();
    window.location.reload();
  } catch (err) {
    setSelectError(errMessage(err));
    setSelecting(null);
  }
}

// waitForRestart polls /api/setup/status until the server answers again
// (the new process may take a moment to bind the port after the old one
// exits) or gives up after a generous timeout, at which point the caller's
// reload happens anyway — worst case the user sees the same "not ready
// yet" state momentarily and can retry.
async function waitForRestart(): Promise<void> {
  const deadline = Date.now() + 15_000;
  // The old process is still alive for ~300ms after responding to
  // /api/setup/select (see selectWorkspaceFunc's restart delay), so the
  // first few polls are expected to still hit it — keep polling regardless
  // of individual failures until the deadline.
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 400));
    try {
      await apiFetchJSON<SetupStatus>("/api/setup/status", { silent: true });
      return;
    } catch {
      // still restarting
    }
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

// loadAgents reflects live filesystem status for the given scope — hits
// GET /api/setup/agents fresh every call rather than caching, so switching
// back to the UI after running `polyflow setup` from a terminal (or vice
// versa) always shows the current on-disk state, not a stale snapshot.
async function loadAgents(scope: SetupScope = agentScope()): Promise<void> {
  setAgentScope(scope);
  setAgentsLoading(true);
  setAgentsError(null);
  try {
    const res = await apiFetchJSON<{ scope: string; agents: SetupAgentInfo[] }>(
      `/api/setup/agents?scope=${encodeURIComponent(scope)}`,
      { silent: true },
    );
    setAgents(res.agents);
  } catch (err) {
    setAgentsError(errMessage(err));
  } finally {
    setAgentsLoading(false);
  }
}

// omitKey returns a shallow copy of rec without key — used to clear a
// stale result from the *other* action's map when apply/remove flips state
// (e.g. a lingering "removed" message shouldn't survive a fresh apply).
function omitKey<T>(rec: Record<string, T>, key: string): Record<string, T> {
  const { [key]: _omitted, ...rest } = rec;
  return rest;
}

async function applyAgent(name: string, scope: SetupScope = agentScope()): Promise<boolean> {
  setApplyingAgent(name);
  setAgentApplyErrors((prev) => ({ ...prev, [name]: "" }));
  setAgentRemoveResults((prev) => omitKey(prev, name));
  try {
    const result = await apiFetchJSON<SetupAgentApplyResult>("/api/setup/agent", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent: name, scope }),
      silent: true,
    });
    setAgentApplyResults((prev) => ({ ...prev, [name]: result }));
    await loadAgents(scope);
    return true;
  } catch (err) {
    setAgentApplyErrors((prev) => ({ ...prev, [name]: errMessage(err) }));
    return false;
  } finally {
    setApplyingAgent(null);
  }
}

// removeAgent is applyAgent's inverse: DELETE /api/setup/agent unregisters
// the MCP server, unwires polyflow's hook entries, and strips the
// CLAUDE.md/AGENTS.md nudge block, mirroring `polyflow setup --remove`.
async function removeAgent(name: string, scope: SetupScope = agentScope()): Promise<boolean> {
  setRemovingAgent(name);
  setAgentRemoveErrors((prev) => ({ ...prev, [name]: "" }));
  setAgentApplyResults((prev) => omitKey(prev, name));
  try {
    const result = await apiFetchJSON<SetupAgentRemoveResult>("/api/setup/agent", {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent: name, scope }),
      silent: true,
    });
    setAgentRemoveResults((prev) => ({ ...prev, [name]: result }));
    await loadAgents(scope);
    return true;
  } catch (err) {
    setAgentRemoveErrors((prev) => ({ ...prev, [name]: errMessage(err) }));
    return false;
  } finally {
    setRemovingAgent(null);
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
  registryEntries,
  registryLoading,
  registryError,
  selecting,
  selectError,
  loadRegistry,
  selectWorkspace,
  agentScope,
  agents,
  agentsLoading,
  agentsError,
  applyingAgent,
  agentApplyResults,
  agentApplyErrors,
  removingAgent,
  agentRemoveResults,
  agentRemoveErrors,
  loadAgents,
  applyAgent,
  removeAgent,
  reset: () => {
    setStatus(null);
    setChecking(true);
    setDiscovering(false);
    setDiscoverError(null);
    setDiscovered(null);
    setApplying(false);
    setApplyError(null);
    setRegistryEntries([]);
    setRegistryLoading(false);
    setRegistryError(null);
    setSelecting(null);
    setSelectError(null);
    setAgents([]);
    setAgentsError(null);
    setApplyingAgent(null);
    setAgentApplyResults({});
    setAgentApplyErrors({});
    setRemovingAgent(null);
    setAgentRemoveResults({});
    setAgentRemoveErrors({});
  },
};
