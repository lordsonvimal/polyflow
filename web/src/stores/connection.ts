import { createSignal } from "solid-js";

export type ConnectionState = "connecting" | "connected" | "disconnected";

const [state, setState] = createSignal<ConnectionState>("connecting");
const [retryIn, setRetryIn] = createSignal(0);

const INITIAL_RETRY_MS = 1000;
const MAX_RETRY_MS = 30_000;

let es: EventSource | undefined;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
let countdownTimer: ReturnType<typeof setInterval> | undefined;
let retryDelay = INITIAL_RETRY_MS;
let stopped = true;

type EventListener = (data: { type: string; [k: string]: unknown }) => void;
const eventListeners = new Set<EventListener>();

// UO.1: distinct from onEvent's per-message stream — fires once per
// reconnect (an onopen that isn't the very first connection of this
// session), so a consumer like the tool-call log can tell "we may have
// missed messages" from "this is just startup".
type ReconnectListener = () => void;
const reconnectListeners = new Set<ReconnectListener>();
let hasConnectedOnce = false;

function clearTimers(): void {
  clearTimeout(retryTimer);
  clearInterval(countdownTimer);
}

function scheduleReconnect(): void {
  clearTimers();
  let remaining = Math.ceil(retryDelay / 1000);
  setRetryIn(remaining);
  countdownTimer = setInterval(() => {
    remaining -= 1;
    setRetryIn(Math.max(0, remaining));
  }, 1000);
  retryTimer = setTimeout(() => {
    retryDelay = Math.min(retryDelay * 2, MAX_RETRY_MS);
    connect();
  }, retryDelay);
}

function connect(): void {
  if (stopped || typeof EventSource === "undefined") return;
  setState("connecting");
  try {
    es = new EventSource("/api/events");
  } catch {
    setState("disconnected");
    scheduleReconnect();
    return;
  }
  es.onopen = () => {
    setState("connected");
    if (hasConnectedOnce) reconnectListeners.forEach((l) => l());
    hasConnectedOnce = true;
    retryDelay = INITIAL_RETRY_MS;
    clearTimers();
  };
  es.onerror = () => {
    es?.close();
    setState("disconnected");
    scheduleReconnect();
  };
  es.onmessage = (ev) => {
    try {
      const data = JSON.parse(ev.data);
      eventListeners.forEach((l) => l(data));
    } catch {
      // Non-JSON / malformed payload — ignore rather than crash the stream.
    }
  };
}

export const connectionStore = {
  state,
  retryIn,
  start: () => {
    stopped = false;
    retryDelay = INITIAL_RETRY_MS;
    hasConnectedOnce = false;
    connect();
  },
  stop: () => {
    stopped = true;
    es?.close();
    es = undefined;
    clearTimers();
  },
  reconnectNow: () => {
    clearTimers();
    retryDelay = INITIAL_RETRY_MS;
    connect();
  },
  // Subscribe to parsed SSE payloads (e.g. {type:"graph_updated"}); returns an unsubscribe fn.
  onEvent: (l: EventListener): (() => void) => {
    eventListeners.add(l);
    return () => eventListeners.delete(l);
  },
  onReconnect: (l: ReconnectListener): (() => void) => {
    reconnectListeners.add(l);
    return () => reconnectListeners.delete(l);
  },
};
