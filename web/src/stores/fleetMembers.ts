// GR.6: fleet-member switcher — GET /api/fleet/services (Tier GR's
// git-backed registry membership list, distinct from FR.7's
// stores/fleetStatus.ts, which still reports the older services/<name>/
// graph.db model) and POST /api/fleet/active. An empty services list means
// this workspace isn't a registered Tier-GR fleet member, not an error.
import { createSignal } from "solid-js";
import { apiFetchJSON, ApiError } from "../lib/apiFetch";
import { notificationsStore } from "../stores/notifications";
import { connectionStore } from "./connection";

export interface FleetMemberRow {
  service: string;
  active: boolean;
}

const [services, setServices] = createSignal<FleetMemberRow[]>([]);
const [loading, setLoading] = createSignal(false);
const [switching, setSwitching] = createSignal(false);

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

async function load(): Promise<void> {
  setLoading(true);
  try {
    const data = await apiFetchJSON<{ services: FleetMemberRow[] }>("/api/fleet/services", { silent: true });
    setServices(data.services ?? []);
  } catch (err) {
    notificationsStore.add({
      id: `fleet-members-load-err-${Date.now()}`,
      kind: "error",
      message: "Failed to load fleet members",
      detail: parseErrorMessage(err),
    });
  } finally {
    setLoading(false);
  }
}

// setActive ensures one more fleet member is resolved (cloning it on demand
// server-side, GR.1, if this machine doesn't have it yet) and merged into
// the fleet-wide view — GR.6 revised: every resolved member stays merged
// simultaneously, so this widens the view rather than switching away from
// whichever member was previously active. Can take a few seconds on a first
// clone — the caller should show switching() while it resolves. A
// successful call's graph_updated broadcast (handled by every other
// store's own onEvent listener, e.g. stores/tree.ts) is what actually
// refreshes the rest of the UI; this function only flips the target
// member's own active flag, leaving every other row untouched.
async function setActive(service: string): Promise<void> {
  setSwitching(true);
  try {
    await apiFetchJSON("/api/fleet/active", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ service }),
    });
    setServices((rows) => rows.map((r) => (r.service === service ? { ...r, active: true } : r)));
  } catch (err) {
    notificationsStore.add({
      id: `fleet-members-switch-err-${Date.now()}`,
      kind: "error",
      message: `Failed to load ${service}`,
      detail: parseErrorMessage(err),
    });
  } finally {
    setSwitching(false);
  }
}

let unsubscribe: (() => void) | undefined;

export const fleetMembersStore = {
  services,
  loading,
  switching,
  load,
  setActive,
  reset: () => {
    setServices([]);
    setLoading(false);
    setSwitching(false);
  },
  // Called once at app startup (mirrors stores/tree.ts's own subscription)
  // so a fleet switch triggered from another tab/session is reflected here
  // too, not just the tab that issued it.
  subscribe: () => {
    unsubscribe?.();
    unsubscribe = connectionStore.onEvent((ev) => {
      if (ev.type !== "graph_updated") return;
      load();
    });
    return unsubscribe;
  },
};
