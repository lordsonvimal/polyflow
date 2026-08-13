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
    retryDelay = INITIAL_RETRY_MS;
    clearTimers();
  };
  es.onerror = () => {
    es?.close();
    setState("disconnected");
    scheduleReconnect();
  };
}

export const connectionStore = {
  state,
  retryIn,
  start: () => {
    stopped = false;
    retryDelay = INITIAL_RETRY_MS;
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
};
