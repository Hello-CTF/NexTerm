// 事件订阅（§6.3）：Rust → 前端事件统一走 listen。
// 演示模式下没有内核，`listenEvent` 与两个 `create*Channel` 改走本地事件总线，
// 调用方（main.tsx / XtermView / AiSidebar / FileBrowser / DockerPanel）无需分支。
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { Channel } from "@tauri-apps/api/core";
import { DEMO, subscribe } from "../demo";

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
  return listen<T>(event, (e) => handler(e.payload));
}
