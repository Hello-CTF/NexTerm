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
import { newBinaryChannel, newJsonChannel, subscribeEvent } from "./webTransport";

export const EVENTS = {
  sessionStatus: "session://status",
  terminalExit: "terminal://exit",
  terminalThrottled: "terminal://throttled",
  fsProgress: "fs://progress",
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
