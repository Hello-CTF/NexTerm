
import { clientId, wsUrl } from "./env";
import { wsAuthProtocols } from "./serverAuth";

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
}

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
  const entry: ChannelEntry = { ch, ws: null, disposed: false, retry: 0, hasOpened: false };
  channels.set(ch.id, entry);
  connect(entry);
}

export function disposeChannel(channelId: string) {
  const entry = channels.get(channelId);
  if (!entry) return;
  entry.disposed = true;
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
    ws = new WebSocket(wsUrl(`/ws/channel/${encodeURIComponent(entry.ch.id)}`), wsAuthProtocols());
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

function scheduleReconnect(entry: ChannelEntry) {
  entry.retry = Math.min(entry.retry + 1, 6);
  const delay = Math.min(300 * 2 ** (entry.retry - 1), 8000);
  window.setTimeout(() => connect(entry), delay);
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
    ws = new WebSocket(wsUrl(`/ws/events${query}`), wsAuthProtocols());
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
          console.error("[NexTerm] resync 处理器抛出异常", e);
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
        console.error("[NexTerm] 事件处理器抛出异常", parsed.event, e);
      }
    }
  };
  ws.onclose = () => {
    events.ws = null;
    scheduleEventsReconnect();
  };
}

function scheduleEventsReconnect() {
  events.retry = Math.min(events.retry + 1, 6);
  const delay = Math.min(300 * 2 ** (events.retry - 1), 8000);
  window.setTimeout(connectEvents, delay);
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
