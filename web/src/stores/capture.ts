// UO.6: Runtime capture UI — record/stop/ingest OTLP evidence from the
// browser and prompt to fuse it into the graph via the existing index job
// (UB.7's fusion_hint: fusion only happens at index time, never here).
import { createSignal } from "solid-js";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";
import { connectionStore } from "./connection";
import { notificationsStore } from "./notifications";
import { jobsStore } from "./jobs";

export type CaptureState = "idle" | "starting" | "active" | "stopping";

export interface ActiveCaptureSession {
  session: string;
  since: string;
  spans_received: number;
  http_port: number;
  grpc_port: number;
}

// internal/evidence/trace_ingest.SessionInfo has no json tags, so its wire
// shape uses Go's exported field names verbatim.
export interface CaptureSessionInfo {
  Name: string;
  StartedAt: string;
  StoppedAt?: string | null;
  SpanCount: number;
  Age: string;
}

export interface CaptureStatus {
  active: ActiveCaptureSession[];
  sessions: CaptureSessionInfo[];
}

export interface FusePrompt {
  session: string;
  spanCount: number;
}

const [captureState, setCaptureState] = createSignal<CaptureState>("idle");
const [activeSessions, setActiveSessions] = createSignal<ActiveCaptureSession[]>([]);
const [sessions, setSessions] = createSignal<CaptureSessionInfo[]>([]);
const [fusePrompt, setFusePrompt] = createSignal<FusePrompt | null>(null);
// The session this browser tab started (vs. one discovered active from the
// CLI or another tab) — used only to default the stop button's target.
const [uiSession, setUiSession] = createSignal<string | null>(null);

const STATUS_POLL_MS = 2000;
let pollTimer: ReturnType<typeof setInterval> | undefined;

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

async function refreshStatus(): Promise<void> {
  try {
    const data = await apiFetchJSON<CaptureStatus>("/api/capture/status", { silent: true });
    setActiveSessions(data.active ?? []);
    setSessions(data.sessions ?? []);
    if ((data.active ?? []).length > 0) {
      if (captureState() === "idle") setCaptureState("active");
    } else if (captureState() === "active") {
      setCaptureState("idle");
      setUiSession(null);
    }
  } catch {
    // transient — the next poll tick retries
  }
}

function startPolling(): void {
  if (pollTimer) return;
  pollTimer = setInterval(() => void refreshStatus(), STATUS_POLL_MS);
}

function stopPolling(): void {
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = undefined;
}

async function start(session: string, httpPort = 4318, grpcPort = 4317): Promise<boolean> {
  setCaptureState("starting");
  try {
    const res = await apiFetch("/api/capture/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session, http_port: httpPort, grpc_port: grpcPort }),
      silent: true,
    });
    const data = (await res.json()) as { session: string; http_port: number; grpc_port: number };
    setUiSession(data.session);
    setCaptureState("active");
    await refreshStatus();
    startPolling();
    return true;
  } catch (err) {
    setCaptureState("idle");
    if (err instanceof ApiError && err.status === 409) {
      let message = "Capture port already in use";
      let detail: string | undefined;
      try {
        const body = JSON.parse(err.body) as { error?: string; port?: number };
        if (body.port) message = `Port ${body.port} already in use`;
        detail = body.error;
      } catch {
        // fall through to the generic message above
      }
      notificationsStore.add({ id: `capture-conflict-${Date.now()}`, kind: "error", message, detail });
      return false;
    }
    notificationsStore.add({
      id: `capture-start-err-${Date.now()}`,
      kind: "error",
      message: "Failed to start capture",
      detail: parseErrorMessage(err),
    });
    return false;
  }
}

async function stop(session?: string): Promise<void> {
  const target = session ?? uiSession() ?? activeSessions()[0]?.session;
  if (!target) return;
  setCaptureState("stopping");
  try {
    const data = await apiFetchJSON<{ session: string; finalized: boolean; fusion_hint: string }>("/api/capture/stop", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session: target }),
      silent: true,
    });
    setUiSession(null);
    const priorSpanCount = activeSessions().find((s) => s.session === target)?.spans_received ?? 0;
    await refreshStatus();
    const info = sessions().find((s) => s.Name === data.session);
    setFusePrompt({ session: data.session, spanCount: info?.SpanCount ?? priorSpanCount });
  } catch (err) {
    notificationsStore.add({
      id: `capture-stop-err-${Date.now()}`,
      kind: "error",
      message: "Failed to stop capture",
      detail: parseErrorMessage(err),
    });
  } finally {
    setCaptureState(activeSessions().length > 0 ? "active" : "idle");
  }
}

async function ingestDump(file: File, session?: string): Promise<void> {
  const form = new FormData();
  form.append("file", file);
  if (session) form.append("session", session);
  try {
    const data = await apiFetchJSON<{ session: string; span_count: number; fusion_hint: string }>("/api/capture/ingest", {
      method: "POST",
      body: form,
      silent: true,
    });
    notificationsStore.add({
      id: `capture-ingest-${Date.now()}`,
      kind: "success",
      message: `Ingested ${data.span_count} span${data.span_count === 1 ? "" : "s"}`,
    });
    setFusePrompt({ session: data.session, spanCount: data.span_count });
    await refreshStatus();
  } catch (err) {
    notificationsStore.add({
      id: `capture-ingest-err-${Date.now()}`,
      kind: "error",
      message: "Failed to ingest OTLP dump",
      detail: parseErrorMessage(err),
    });
  }
}

function dismissFusePrompt(): void {
  setFusePrompt(null);
}

function fuseNow(): void {
  setFusePrompt(null);
  void jobsStore.startIndex(false);
}

connectionStore.onEvent((ev) => {
  if (ev.type !== "capture_progress") return;
  const session = (ev as { session?: string }).session;
  const spansReceived = (ev as { spans_received?: number }).spans_received;
  if (!session || spansReceived === undefined) return;
  setCaptureState((prev) => (prev === "idle" ? "active" : prev));
  setActiveSessions((prev) => {
    if (!prev.some((s) => s.session === session)) return prev;
    return prev.map((s) => (s.session === session ? { ...s, spans_received: spansReceived } : s));
  });
});

export const captureStore = {
  captureState,
  activeSessions,
  sessions,
  fusePrompt,
  uiSession,
  refreshStatus,
  startPolling,
  stopPolling,
  start,
  stop,
  ingestDump,
  dismissFusePrompt,
  fuseNow,
  // Test-only: module-level singleton reset, same pattern as jobsStore.reset.
  reset: () => {
    setCaptureState("idle");
    setActiveSessions([]);
    setSessions([]);
    setFusePrompt(null);
    setUiSession(null);
    stopPolling();
  },
};
