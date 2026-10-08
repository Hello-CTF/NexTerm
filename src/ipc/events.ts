// 事件订阅（§6.3）：Rust → 前端事件统一走 listen。
//
// 三种运行环境各有一条实现，调用方（main.tsx / XtermView / AiSidebar /
// FileBrowser / DockerPanel）无需分支：
//
// | 环境 | listen | Channel |
// |---|---|---|
// | 桌面 | Tauri `listen` | Tauri `Channel` |
// | 服务端 | `/ws/events` | `/ws/channel/{id}` |
// | 演示 | 本地事件总线 | 内存通道 |
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { Channel } from "@tauri-apps/api/core";
import { DEMO, WEB, subscribe } from "../demo";
import { newBinaryChannel, newJsonChannel, onChannelReopen as onWsChannelReopen, disposeChannel as disposeWsChannel, subscribeEvent } from "./webTransport";

export const EVENTS = {
  sessionStatus: "session://status",
  terminalExit: "terminal://exit",
  terminalThrottled: "terminal://throttled",
  /**
   * 终端控制权 / 观看人数 / 进程结束状态变化。
   *
   * 与 Rust 侧 `events::TERMINAL_CONTROL` 必须一致。**全局广播**：一次事件发到所有
   * 终端，各端按 `payload.tabId` 自己过滤；所以订阅方不能假定"这条事件是我这个标签的"。
   */
  terminalControl: "terminal://control",
  fsProgress: "fs://progress",
  /**
   * 应用内更新的下载进度。与 Rust 侧 `events::UPDATE_PROGRESS` 必须一致。
   *
   * 只在用户点了「立即更新」之后才会有事件 —— 检查更新本身是不发事件的，
   * 它的结果直接由 `app_update_check` 的返回值给出。
   */
  updateProgress: "update://progress",
  dockerStats: "docker://stats",
  aiEvent: "ai://event",
  appError: "app://error",
} as const;

export interface SessionStatusEvent {
  sessionId: string;
  status: string;
  error: string | null;
}

export interface TerminalExitEvent {
  tabId: string;
  exitCode: number | null;
}

/**
 * `update://progress` 的 payload（camelCase，与 Rust `UpdateProgressPayload` 对齐）。
 *
 * `total` 可能是 `null`：不是所有镜像都回 `Content-Length`。缺了它要显示**不确定**
 * 进度条，不能按 0% 画 —— 那看着像卡死了，用户会去杀进程。
 */
export interface UpdateProgressEvent {
  downloaded: number;
  total: number | null;
}

/**
 * `terminal://control` 的 payload（camelCase，与 Rust `TerminalControlPayload` 对齐）。
 *
 * 五个字段每次都给全，前端按同一份快照覆盖本地状态即可，不必做增量推断。
 * `subscribers` 是内核原值（**含收到事件的那一端自己**），与 `terminal_list` 同口径；
 * 它是**通道数**（同一台设备开两个页面就 +2），不是设备数。
 * `viewers` 是**按设备去重**后的观看设备数 —— 界面上「N 个设备正在观看」用它。
 */
export interface TerminalControlEvent {
  tabId: string;
  /** 当前持权者的 clientId；null = 无人持权（此时谁都不能敲）。 */
  controller: string | null;
  /** 通道数（含自己）：同一台设备多开一个页面就会 +1。 */
  subscribers: number;
  /** 观看设备数（按 clientId 去重）：同一台设备多开页面不重复计数。 */
  viewers: number;
  exited: boolean;
}

export interface FsProgressEvent {
  taskId: string;
  transferred: number;
  total: number;
  done: boolean;
}

/**
 * 演示模式下的"通道"：只需要 `onmessage` 这一个可赋值字段。
 *
 * 为什么不能直接 `new Channel()`：Tauri 的 Channel 构造函数会调
 * `transformCallback`，它依赖 `window.__TAURI_INTERNALS__`，纯浏览器里会抛。
 */
function demoChannel<T>(): Channel<T> {
  const ch = {
    onmessage: (_msg: unknown) => undefined,
    toJSON() {
      return null;
    },
  };
  return ch as unknown as Channel<T>;
}

/** 终端二进制通道：接收 PTY 原始字节。 */
export function createBinaryChannel(
  onBytes: (data: Uint8Array) => void,
): Channel<unknown> {
  if (DEMO) {
    const ch = demoChannel<unknown>();
    ch.onmessage = (raw: unknown) => decodeBytes(raw, onBytes);
    return ch;
  }
  if (WEB) {
    const ch = newBinaryChannel();
    ch.onmessage = (raw: unknown) => decodeBytes(raw, onBytes);
    return ch as unknown as Channel<unknown>;
  }
  const channel = new Channel<unknown>();
  channel.onmessage = (raw) => decodeBytes(raw, onBytes);
  return channel;
}

/**
 * 取通道 id（只有服务端模式有）。
 *
 * 多端同看时 `terminal_detach` **必须**只摘自己那一条通道，否则一台设备切走标签
 * 会把所有其他设备的推送一起掐掉 —— 而它们那边看起来只是"画面不动了"，极难排查。
 * 桌面模式没有这个概念（整个进程一个视图），返回 `undefined` 让调用方走
 * 「清空全部」的旧语义。
 *
 * 判据是「是不是字符串」而不是「有没有 `id` 字段」：Tauri 的 `Channel` 也有 `id`，
 * 但那是个数字、且属于框架内部，拿它当通道 id 用会静默错配。
 */
export function channelIdOf(ch: unknown): string | undefined {
  const id = (ch as { id?: unknown } | null)?.id;
  return typeof id === "string" ? id : undefined;
}

/**
 * 订阅「该终端通道的 WS 已重连」。桌面 / 演示模式没有 WS 通道，
 * 连接不会断 ⇒ 直接返回空退订函数。
 *
 * 为什么要包一层：调用方（XtermView）只需要知道「通道重开时通知我」，
 * 不该知道 WEB 分支或 webTransport 的存在 —— 否则每个使用通道的组件都要
 * 自己写一遍形态判断，漏一处就是桌面端行为被意外改变。
 */
export function onChannelReopen(ch: unknown, cb: () => void): () => void {
  const id = channelIdOf(ch);
  if (!WEB || id === undefined) return () => {};
  return onWsChannelReopen(id, cb);
}

/**
 * 关闭一条通道，让它对应的 WS 收摊、不再重连。
 *
 * 与 `onChannelReopen` 同款形态无关包装：调用方只管「这条通道用完了」，
 * 不该知道 WEB 分支或 `webTransport` 的存在。桌面 / 演示模式没有 WS 通道，
 * 天然是空操作。
 *
 * ⚠️ 只能在该通道**彻底没有消费者**之后调用 —— 服务端会把未认领的帧缓存进
 * `pending`，作业没结束就关会让输出静默丢失。
 */
export function disposeChannel(ch: unknown): void {
  const id = channelIdOf(ch);
  if (!WEB || id === undefined) return;
  disposeWsChannel(id);
}

function decodeBytes(raw: unknown, onBytes: (data: Uint8Array) => void) {
  if (raw instanceof ArrayBuffer) {
    onBytes(new Uint8Array(raw));
  } else if (raw instanceof Uint8Array) {
    onBytes(raw);
  } else if (Array.isArray(raw)) {
    onBytes(new Uint8Array(raw as number[]));
  } else if (typeof raw === "string") {
    onBytes(new TextEncoder().encode(raw));
  }
}

/** AI 事件通道。 */
export function createAiChannel(onEvent: (event: Record<string, unknown>) => void): Channel<unknown> {
  const push = (raw: unknown) => {
    if (raw && typeof raw === "object") {
      onEvent(raw as Record<string, unknown>);
    } else if (typeof raw === "string") {
      try {
        onEvent(JSON.parse(raw) as Record<string, unknown>);
      } catch {
        // 非 JSON 字符串按 Delta 处理
        onEvent({ type: "delta", text: raw });
      }
    }
  };
  if (DEMO) {
    const ch = demoChannel<unknown>();
    ch.onmessage = push;
    return ch;
  }
  if (WEB) {
    const ch = newJsonChannel();
    ch.onmessage = (raw: unknown) => push(raw);
    return ch as unknown as Channel<unknown>;
  }
  const channel = new Channel<unknown>();
  channel.onmessage = push;
  return channel;
}

export function listenEvent<T>(
  event: string,
  handler: (payload: T) => void,
): Promise<UnlistenFn> {
  if (DEMO) {
    const off = subscribe(event, (payload) => handler(payload as T));
    return Promise.resolve(off);
  }
  if (WEB) {
    const off = subscribeEvent(event, (payload) => handler(payload as T));
    // 与 Tauri 的 `listen` 一样返回**异步**的取消函数，调用方写法不必分支。
    return Promise.resolve(off);
  }
  return listen<T>(event, (e) => handler(e.payload));
}
