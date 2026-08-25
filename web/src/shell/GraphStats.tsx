import { createSignal, onMount } from "solid-js";
import { apiFetchJSON } from "../lib/apiFetch";

export default function GraphStats() {
  const [stats, setStats] = createSignal("--n/--e");

  onMount(async () => {
    try {
      const d = await apiFetchJSON<{ nodes: number; edges: number }>("/api/stats", { silent: true });
      setStats(`${d.nodes}n/${d.edges}e`);
    } catch {
      setStats("--n/--e");
    }
  });

  return <span class="text-xs text-neutral-400 font-mono">{stats()}</span>;
}
