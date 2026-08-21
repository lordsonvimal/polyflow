// FR.7: Fleet status panel — GET /api/fleet/status, one row per configured
// service sourced from that service's own services/<name>/graph.db (FR.2),
// independent of the merged fleet DB's single last_indexed timestamp.
import { createSignal } from "solid-js";
import { apiFetchJSON, ApiError } from "../lib/apiFetch";
import { notificationsStore } from "../stores/notifications";

export interface FleetServiceStatus {
  service: string;
  indexed_at?: string;
  node_count: number;
  edge_count: number;
  indexed: boolean;
}

const [services, setServices] = createSignal<FleetServiceStatus[]>([]);
const [loading, setLoading] = createSignal(false);

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
    const data = await apiFetchJSON<{ services: FleetServiceStatus[] }>("/api/fleet/status", { silent: true });
    setServices(data.services ?? []);
  } catch (err) {
    notificationsStore.add({
      id: `fleet-status-load-err-${Date.now()}`,
      kind: "error",
      message: "Failed to load fleet status",
      detail: parseErrorMessage(err),
    });
  } finally {
    setLoading(false);
  }
}

export const fleetStatusStore = {
  services,
  loading,
  load,
  reset: () => {
    setServices([]);
    setLoading(false);
  },
};
