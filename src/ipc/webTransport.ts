
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
      // 单个订阅者出错不该影响其他订阅者，也不该让 WS 的重连流程炸掉。
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
        // 页面正在销毁，忽略
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

export function newJsonChannel(): WebChannel<unknown> {
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
      // 可能已关闭，或页面正在销毁
    }
  }
}

function connect(entry: ChannelEntry) {
  if (entry.disposed) return;
  let ws: WebSocket;
  try {
    ws = new WebSocket(wsUrl(`/ws/channel/${encodeURIComponent(entry.ch.id)}`));
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
    // onerror 之后必定跟 onclose；重连只放在 onclose，避免双触发。
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
  subs: Map<string, Set<(payload: unknown) => void>>;
}

const events: EventsEntry = { ws: null, retry: 0, subs: new Map() };
let eventsStarted = false;

function connectEvents() {
  if (events.ws) return;
  let ws: WebSocket;
  try {
    ws = new WebSocket(wsUrl("/ws/events"));
  } catch {
    scheduleEventsReconnect();
    return;
  }
  events.ws = ws;
  trackSocket(ws);
  ws.onopen = () => {
    events.retry = 0;
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data !== "string") return;
    let parsed: { event?: string; payload?: unknown };
    try {
      parsed = JSON.parse(ev.data) as { event?: string; payload?: unknown };
    } catch {
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
