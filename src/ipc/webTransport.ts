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

import { clientId, wsUrl } from "./env";

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
  /** 这条通道的 WS 是否曾经 open 过 —— 用来区分「首次连接」与「断线重连」。 */
  hasOpened: boolean;
}

const channels = new Map<string, ChannelEntry>();
let channelSeq = 0;

/**
 * 「这条通道的 WS 已重新连上」的订阅者。键是通道 id。
 *
 * 为什么按 id 而不是按 entry / channel 对象作键：通道 id 是 `WebChannel` 唯一
 * 对外可辨识的标识（`toJSON()` 就是它），组件侧拿到的也只是 id 字符串。
 * 用对象作键会让调用方不得不持有 entry 引用，把模块内部结构泄漏出去。
 */
const reopenSubs = new Map<string, Set<() => void>>();

/**
 * 订阅「该通道的 WS 重连成功」。
 *
 * ⚠️ **只在重开时触发，首次 open 不触发** —— 首次的订阅登记由 attach 自身完成，
 * 再触发一次会变成重复 attach。
 *
 * 返回退订函数（组件卸载时**必须**调用，否则闭包会留住已卸载组件里的回调）。
 */
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
  // 先拷贝再遍历：回调里可能退订（会改动这个 Set）。
  for (const cb of [...subs]) {
    try {
      cb();
    } catch {
      // 单个订阅者出错不该影响其他订阅者，也不该让 WS 的重连流程炸掉。
    }
  }
}

/**
 * 本模块当前打开的 WS 连接集合。
 *
 * 唯一用途是 `pagehide` 时的兜底显式关闭（见 `ensurePagehideHook`）。
 * 增删是配对的 —— 登记时 `add`、连接触发 `close` 事件时 `delete` ——
 * 所以正常生命周期下集合不会随重连/新建通道而泄漏增长。
 */
const openSockets = new Set<WebSocket>();
let pagehideHooked = false;

/**
 * 登记一条 WS，并挂上「关闭即摘除」的监听。
 *
 * 用 `addEventListener("close")` 而不是包 `onclose`：本模块的调用方
 * （`connect` / `connectEvents`）自己也要在 `onclose` 里做重连，包一层容易
 * 在后续维护中被覆盖掉；独立监听互不干扰，且远端关闭与本地 `close()` 都会触发。
 */
function trackSocket(ws: WebSocket) {
  openSockets.add(ws);
  ws.addEventListener("close", () => {
    openSockets.delete(ws);
  });
  ensurePagehideHook();
}

/**
 * 页面离开时的兜底：把本模块打开的所有连接显式关闭。
 *
 * # 为什么需要它
 *
 * 服务端摘订阅者的正常通路是「浏览器关 WS ⇒ 服务端 hub 的 `on_channel_closed`
 * 回调」，绝大多数情况都靠这条。但「页面离开是否一定触发 WS 关闭」取决于浏览器
 * 实现（headless Chrome 下实测 `Page.navigate` 离开并不释放，必须显式 `close()`
 * 或销毁整个 page target）。这里不取代正常通路，只把「页面确定要走」这件事变成
 * **确定性的显式关闭**，让服务端订阅者计数不依赖浏览器的关闭时机。
 *
 * # 为什么不按 `persisted` 跳过
 *
 * 这里曾经是 `if (e.persisted) return;`，理由是「persisted === true 表示页面
 * 进了往返缓存，稍后用户按『后退』会原样恢复，主动 close 会掐断仍要用的连接」。
 * 实机对照（headless Chrome + CDP，用 beacon 实测 `persisted` 值）推翻了它：
 *
 * 1. 导航离开应用页时 `pagehide` **总是**以 `persisted === true` 触发 ——
 *    `Page.navigate` 到 `about:blank`、跨源真实页面、`data:` URL 三种目的地都
 *    如此。于是那条守卫把整条兜底变成了空操作：给 `WebSocket.prototype.close`
 *    插桩后观测到 bfcache 路径下 close 调用 0 次。
 * 2. bfcache 实际上**并没有保住 WebSocket**：后退恢复的那一刻就能观测到 WS
 *    关闭事件，应用随即用同一 channel id 重连。也就是说「跳过 close 以免掐断
 *    bfcache 里的连接」保护的是一个不存在的东西。
 * 3. 代价却是真实的：页面冻结期间服务端那条订阅一直挂着，订阅者计数虚高，
 *    会给用户看到错误的「还有 N 个设备正在观看」。
 *
 * 收益为零、代价明确，所以不论 `persisted` 真假一律关闭。
 *
 * # 复访条件
 *
 * 若在真实（非 headless）浏览器上确认 bfcache 确实能保住 WebSocket，并且
 * 用户后退恢复后该连接仍然可用（而不是浏览器已经把它关掉、由应用重连出来的），
 * 则「按 `persisted` 跳过」这个决定需要重新评估。
 *
 * 幂等：只注册一次，而非每建一条连接都 add 一个 listener。
 */
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

/**
 * 通道 id：`<客户端id>-c<序号>-<随机>`。
 *
 * 带上客户端前缀是为了排查时能一眼看出「这条通道是哪台设备的」。
 * ⚠️ 控制权的判定**不解析这个字符串** —— 那是脆弱约定，改一次格式就全线失效；
 * 客户端身份是作为独立参数传给命令的（见 `commands.ts` 的 `clientId()` 用法）。
 *
 * ⚠️ 只能用字母数字与连字符：这个 id 要进 `/ws/channel/{id}` 的 URL 路径。
 * 用 `#` 之类的字符会被当成 fragment 截断，症状是「通道永远连不上」。
 */
function newId(): string {
  channelSeq += 1;
  const rnd = Math.random().toString(36).slice(2, 10);
  return `${clientId()}-c${channelSeq}-${rnd}`;
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
  const entry: ChannelEntry = { ch, ws: null, disposed: false, retry: 0, hasOpened: false };
  channels.set(ch.id, entry);
  connect(entry);
}

/**
 * 关闭并注销一条通道：置 `disposed`、关 WS、从 `channels` 表摘掉。
 *
 * 为什么必须有：`disposed` 声明了却从没人置 true，也没有任何导出能关掉通道，
 * 于是组件卸载后那条 WS 一直挂着（`onclose` 会 `scheduleReconnect`）——
 * 每开一个终端标签就永久多一条。实测 `liveChannels` 1→2→…→6，关标签从不回吐。
 *
 * ⚠️ 幂等：重复调用安全（表里没有就直接返回）。
 */
export function disposeChannel(channelId: string) {
  const entry = channels.get(channelId);
  if (!entry) return;
  entry.disposed = true;
  channels.delete(channelId);
  // 退订表按 id 作键；不清的话这条已回收的 id 会连同它的回调一起留住。
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
