// 服务端模式（浏览器 + nexterm-server）的传输层。
//
// # 它替代了桌面下的哪两样东西
//
// | 桌面 Tauri | 这里 |
// |---|---|
// | `ipc::Channel<T>`（PTY 字节 / AI 事件） | `WebChannel` + `/ws/channel/{id}` |
// | `listen()`（`tauri::Emitter` 事件） | `/ws/events` 单连接 + 本地订阅表 |
//
// # 通道 id 为什么能"自动"传上去
//
// `WebChannel.toJSON()` 返回自己的 id。命令参数里的 channel 是**原样交给**
// `JSON.stringify` 的，于是序列化出来就是一个字符串 id —— `call()` 不需要
// 为通道做任何特判，`terminalApi.attach(sessionId, cols, rows, channel)` 这种
// 现有写法一行不用改。
//
// # 连线时序
//
// 通道是**先被创建、再被当成参数发出去**的，这中间 WS 可能还没连上。
// 服务端为此把「还没有 WS 认领的帧」先缓存下来（见 Rust 侧 `server/hub.rs`），
// 所以这里不需要「等 open 再调命令」这种约定 —— 它是个容易漏、漏了就少一屏
// 滚动内容（`terminal_attach` 的回滚）的约定。
//
// 断线重连用**同一个 id**：服务端同样会把断线期间产生的帧缓存起来，
// 重连后一次性补齐。

import { wsUrl } from "./env";

/** 与 Rust 侧 `Payload` 的 JSON 分支对齐：文本帧是 JSON。 */
type ChannelMessage = unknown;

class WebChannel<T> {
  readonly id: string;
  onmessage: (msg: T) => void = () => {};

  constructor() {
    this.id = newId();
  }

  /** 让 `JSON.stringify(args)` 把通道压成一个字符串 id（服务端认这个形状）。 */
  toJSON(): string {
    return this.id;
  }
}

interface ChannelEntry {
  ch: WebChannel<unknown>;
  ws: WebSocket | null;
  disposed: boolean;
  retry: number;
}

const channels = new Map<string, ChannelEntry>();
let channelSeq = 0;

function newId(): string {
  channelSeq += 1;
  const rnd = Math.random().toString(36).slice(2, 10);
  return `c${channelSeq}-${rnd}`;
}

/** 新建一条二进制通道（PTY 输出）。 */
export function newBinaryChannel(): WebChannel<unknown> {
  const ch = new WebChannel<unknown>();
  attach(ch);
  return ch;
}

/** 新建一条结构化事件通道（AI 流）。 */
export function newJsonChannel(): WebChannel<unknown> {
  const ch = new WebChannel<unknown>();
  attach(ch);
  return ch;
}

function attach(ch: WebChannel<unknown>) {
  const entry: ChannelEntry = { ch, ws: null, disposed: false, retry: 0 };
  channels.set(ch.id, entry);
  connect(entry);
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

  ws.onopen = () => {
    entry.retry = 0;
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

/**
 * 文本帧 = JSON（AI 事件）；二进制帧 = 原始字节（PTY）。
 *
 * 空帧返回 `undefined` 让调用方跳过 —— 否则 `onmessage(undefined)` 会让
 * 下游的 `raw instanceof ArrayBuffer` 链全部落空，最后被当成空字符串写进终端。
 */
function decodeFrame(data: unknown): ChannelMessage | undefined {
  if (data instanceof ArrayBuffer) {
    return new Uint8Array(data);
  }
  if (typeof data === "string") {
    if (data.length === 0) return undefined;
    try {
      return JSON.parse(data) as unknown;
    } catch {
      // 不是 JSON 的文本：按纯文本事件交给下游（与桌面版 Channel 的行为一致）。
      return data;
    }
  }
  return data;
}

// ── 结构化事件（替代 tauri `listen`）───────────────────────────────────

interface EventsEntry {
  ws: WebSocket | null;
  retry: number;
  /** event 名 → 订阅者 */
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
        // 一个订阅者抛异常不该让其余订阅者收不到事件。
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

/** 订阅一个服务端事件，返回取消订阅函数。 */
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
