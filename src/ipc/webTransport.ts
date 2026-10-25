
import { clientId, wsUrl } from "./env";

type ChannelMessage = unknown;

class WebChannel<T> {
  readonly id: string;
  onmessage: (msg: T) => void = () => {};

  constructor() {
    this.id = newId();
  }

  toJSON(): string {
    return this.id;
  }
}

interface ChannelEntry {
  ch: WebChannel<unknown>;
  ws: WebSocket | null;
  disposed: boolean;
  retry: number;
  hasOpened: boolean;
  // Upstream terminal input, only usable when the server accepted the input subprotocol.
  input: InputState;
}

// A batch is never re-addressed: it keeps the tab and HTTP writer it was queued with.
interface InputBatch {
  tabId: string;
  http: (data: Uint8Array) => Promise<unknown>;
  chunks: Uint8Array[];
}

interface InputState {
  batches: InputBatch[];
  pendingBytes: number;
  scheduled: boolean;
  httpInFlight: boolean;
  drainTimer: ReturnType<typeof setTimeout> | null;
}

const INPUT_SUBPROTOCOL = "nexterm.input";
const INPUT_FRAME = 1;
const INPUT_MAX_PAYLOAD = 16 * 1024;
const INPUT_MAX_BUFFERED = 256 * 1024;
const INPUT_MAX_PENDING = 4 * 1024 * 1024;
const INPUT_DRAIN_MS = 10;

const channels = new Map<string, ChannelEntry>();
let channelSeq = 0;

const reopenSubs = new Map<string, Set<() => void>>();

export function onChannelReopen(channelId: string, cb: () => void): () => void {
  let set = reopenSubs.get(channelId);
  if (!set) {
    set = new Set();
    reopenSubs.set(channelId, set);
  }
  set.add(cb);
  return () => {
    const s = reopenSubs.get(channelId);
    if (!s) return;
    s.delete(cb);
    if (s.size === 0) reopenSubs.delete(channelId);
  };
}

function notifyChannelReopen(channelId: string) {
  const subs = reopenSubs.get(channelId);
  if (!subs) return;
  for (const cb of [...subs]) {
    try {
      cb();
    } catch {
    }
  }
}

const openSockets = new Set<WebSocket>();
let pagehideHooked = false;

function trackSocket(ws: WebSocket) {
  openSockets.add(ws);
  ws.addEventListener("close", () => {
    openSockets.delete(ws);
  });
  ensurePagehideHook();
}

function ensurePagehideHook() {
  if (pagehideHooked) return;
  pagehideHooked = true;
  window.addEventListener("pagehide", () => {
    for (const s of openSockets) {
      try {
        s.close();
      } catch {
      }
    }
  });
}

function newId(): string {
  channelSeq += 1;
  const rnd = Math.random().toString(36).slice(2, 10);
  return `${clientId()}-c${channelSeq}-${rnd}`;
}

export function newBinaryChannel(): WebChannel<unknown> {
  const ch = new WebChannel<unknown>();
  attach(ch);
  return ch;
}

function attach(ch: WebChannel<unknown>) {
  const entry: ChannelEntry = {
    ch,
    ws: null,
    disposed: false,
    retry: 0,
    hasOpened: false,
    input: { batches: [], pendingBytes: 0, scheduled: false, httpInFlight: false, drainTimer: null },
  };
  channels.set(ch.id, entry);
  connect(entry);
}

export function disposeChannel(channelId: string) {
  const entry = channels.get(channelId);
  if (!entry) return;
  entry.disposed = true;
  if (entry.input.drainTimer !== null) clearTimeout(entry.input.drainTimer);
  entry.input.batches = [];
  entry.input.pendingBytes = 0;
  channels.delete(channelId);
  reopenSubs.delete(channelId);
  const ws = entry.ws;
  entry.ws = null;
  if (ws) {
    try {
      ws.close();
    } catch {
    }
  }
}

function connect(entry: ChannelEntry) {
  if (entry.disposed) return;
  let ws: WebSocket;
  try {
    // Servers that do not know the input subprotocol select "nexterm", and input then stays on the HTTP path.
    ws = new WebSocket(
      wsUrl(`/ws/channel/${encodeURIComponent(entry.ch.id)}?client=${encodeURIComponent(clientId())}`),
      [INPUT_SUBPROTOCOL, "nexterm"],
    );
  } catch {
    scheduleReconnect(entry);
    return;
  }
  ws.binaryType = "arraybuffer";
  entry.ws = ws;
  trackSocket(ws);

  ws.onopen = () => {
    const isReopen = entry.hasOpened;
    entry.hasOpened = true;
    entry.retry = 0;
    if (isReopen) notifyChannelReopen(entry.ch.id);
    flushInput(entry);
  };
  ws.onmessage = (ev) => {
    const msg = decodeFrame(ev.data);
    if (msg !== undefined) entry.ch.onmessage(msg);
  };
  ws.onclose = () => {
    entry.ws = null;
    if (!entry.disposed) scheduleReconnect(entry);
  };
  ws.onerror = () => {
  };
}

/**
 * Sends terminal input for the tab bound to this channel. Writes made in the same task are merged into one frame and
 * flushed in a microtask, so a lone keystroke is not delayed. While the socket is open and has negotiated input it
 * carries every byte, in order; when the socket buffer is full the rest waits in the queue (it never overtakes the
 * buffered frames over HTTP). Only when the socket is not open or not negotiated does `http` carry the bytes, one
 * request in flight per channel, and later writes wait for it so the two paths never reorder. Frames already handed to a
 * socket that then drops are not resent: the server may have applied them and keystrokes must not be double-applied
 * (accepted trade-off: such in-flight frames can be lost). Queued input beyond 4 MiB is dropped with a warning.
 */
export function sendChannelInput(
  channelId: string,
  tabId: string,
  data: Uint8Array,
  http: (data: Uint8Array) => Promise<unknown>,
): void {
  const entry = channels.get(channelId);
  if (!entry || entry.disposed) {
    void http(data).catch(() => undefined);
    return;
  }
  const input = entry.input;
  if (input.pendingBytes + data.length > INPUT_MAX_PENDING) {
    console.warn(`terminal input dropped: ${data.length} bytes exceed the pending input cap`);
    return;
  }
  const last = input.batches[input.batches.length - 1];
  if (last && last.tabId === tabId) {
    last.chunks.push(data);
  } else {
    input.batches.push({ tabId, http, chunks: [data] });
  }
  input.pendingBytes += data.length;
  if (input.scheduled) return;
  input.scheduled = true;
  queueMicrotask(() => {
    input.scheduled = false;
    flushInput(entry);
  });
}

function negotiatedSocket(entry: ChannelEntry): WebSocket | null {
  const ws = entry.ws;
  return ws && ws.readyState === 1 && ws.protocol === INPUT_SUBPROTOCOL ? ws : null;
}

function mergeChunks(chunks: Uint8Array[]): Uint8Array {
  if (chunks.length === 1) return chunks[0];
  const merged = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.length;
  }
  return merged;
}

function flushInput(entry: ChannelEntry) {
  const input = entry.input;
  if (input.drainTimer !== null) {
    clearTimeout(input.drainTimer);
    input.drainTimer = null;
  }
  while (!input.httpInFlight && input.batches.length > 0) {
    const batch = input.batches[0];
    let data = mergeChunks(batch.chunks);
    batch.chunks = [data];
    if (data.length === 0) {
      input.batches.shift();
      continue;
    }
    const ws = negotiatedSocket(entry);
    const tab = new TextEncoder().encode(batch.tabId);
    if (ws && tab.length > 0 && tab.length < 256) {
      let socketFailed = false;
      while (data.length > 0 && ws.bufferedAmount <= INPUT_MAX_BUFFERED) {
        const part = data.subarray(0, INPUT_MAX_PAYLOAD);
        const frame = new Uint8Array(2 + tab.length + part.length);
        frame[0] = INPUT_FRAME;
        frame[1] = tab.length;
        frame.set(tab, 2);
        frame.set(part, 2 + tab.length);
        try {
          ws.send(frame);
        } catch {
          socketFailed = true;
          break;
        }
        data = data.subarray(part.length);
        input.pendingBytes -= part.length;
        batch.chunks = [data];
      }
      if (data.length === 0) {
        input.batches.shift();
        continue;
      }
      if (!socketFailed) {
        // Socket buffer is full: keep order by waiting for it to drain instead of switching to HTTP.
        input.drainTimer = setTimeout(() => {
          input.drainTimer = null;
          flushInput(entry);
        }, INPUT_DRAIN_MS);
        return;
      }
    }
    input.batches.shift();
    input.pendingBytes -= data.length;
    input.httpInFlight = true;
    void batch
      .http(data)
      .catch(() => undefined)
      .finally(() => {
        input.httpInFlight = false;
        flushInput(entry);
      });
  }
}

function scheduleReconnect(entry: ChannelEntry) {
  if (typeof WebSocket === "undefined") return;
  entry.retry = Math.min(entry.retry + 1, 6);
  const delay = Math.min(300 * 2 ** (entry.retry - 1), 8000);
  globalThis.setTimeout(() => connect(entry), delay);
}

function decodeFrame(data: unknown): ChannelMessage | undefined {
  if (data instanceof ArrayBuffer) {
    return new Uint8Array(data);
  }
  if (typeof data === "string") {
    if (data.length === 0) return undefined;
    try {
      return JSON.parse(data) as unknown;
    } catch {
      return data;
    }
  }
  return data;
}

interface EventsEntry {
  ws: WebSocket | null;
  retry: number;
  hasConnected: boolean;
  lastId: number;
  subs: Map<string, Set<(payload: unknown) => void>>;
  resyncSubs: Set<() => void>;
}

const events: EventsEntry = { ws: null, retry: 0, hasConnected: false, lastId: 0, subs: new Map(), resyncSubs: new Set() };
let eventsStarted = false;

export function onEventsResync(cb: () => void): () => void {
  events.resyncSubs.add(cb);
  return () => {
    events.resyncSubs.delete(cb);
  };
}

function connectEvents() {
  if (events.ws) return;
  let ws: WebSocket;
  try {
    const query = events.hasConnected ? `?since=${events.lastId}` : "";
    ws = new WebSocket(wsUrl(`/ws/events${query}`), ["nexterm"]);
  } catch {
    scheduleEventsReconnect();
    return;
  }
  events.ws = ws;
  trackSocket(ws);
  ws.onopen = () => {
    events.retry = 0;
    events.hasConnected = true;
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data !== "string") return;
    let parsed: { id?: number; resync?: boolean; event?: string; payload?: unknown };
    try {
      parsed = JSON.parse(ev.data) as { id?: number; resync?: boolean; event?: string; payload?: unknown };
    } catch {
      return;
    }
    if (typeof parsed.id === "number") {
      events.lastId = parsed.id;
    }
    if (parsed.resync === true) {
      for (const fn of [...events.resyncSubs]) {
        try {
          fn();
        } catch (e) {
          console.error("[NexTerm] Resync handler threw an exception", e);
        }
      }
      return;
    }
    if (!parsed.event) return;
    const set = events.subs.get(parsed.event);
    if (!set) return;
    for (const fn of set) {
      try {
        fn(parsed.payload);
      } catch (e) {
        console.error("[NexTerm] Event handler threw an exception", parsed.event, e);
      }
    }
  };
  ws.onclose = () => {
    events.ws = null;
    scheduleEventsReconnect();
  };
}

function scheduleEventsReconnect() {
  if (typeof WebSocket === "undefined") return;
  events.retry = Math.min(events.retry + 1, 6);
  const delay = Math.min(300 * 2 ** (events.retry - 1), 8000);
  globalThis.setTimeout(connectEvents, delay);
}

export function subscribeEvent(
  event: string,
  handler: (payload: unknown) => void,
): () => void {
  if (!eventsStarted) {
    eventsStarted = true;
    connectEvents();
  }
  let set = events.subs.get(event);
  if (!set) {
    set = new Set();
    events.subs.set(event, set);
  }
  set.add(handler);
  return () => {
    set?.delete(handler);
    if (set && set.size === 0) events.subs.delete(event);
  };
}
