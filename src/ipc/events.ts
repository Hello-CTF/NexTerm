// 事件订阅（§6.3）：Rust → 前端事件统一走 listen。
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { Channel } from "@tauri-apps/api/core";

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

/** 终端二进制通道：接收 PTY 原始字节。 */
export function createBinaryChannel(
  onBytes: (data: Uint8Array) => void,
): Channel<unknown> {
  const channel = new Channel<unknown>();
  channel.onmessage = (raw) => {
    if (raw instanceof ArrayBuffer) {
      onBytes(new Uint8Array(raw));
    } else if (raw instanceof Uint8Array) {
      onBytes(raw);
    } else if (Array.isArray(raw)) {
      onBytes(new Uint8Array(raw as number[]));
    } else if (typeof raw === "string") {
      onBytes(new TextEncoder().encode(raw));
    }
  };
  return channel;
}

/** AI 事件通道。 */
export function createAiChannel(onEvent: (event: Record<string, unknown>) => void): Channel<unknown> {
  const channel = new Channel<unknown>();
  channel.onmessage = (raw) => {
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
  return channel;
}

export function listenEvent<T>(
  event: string,
  handler: (payload: T) => void,
): Promise<UnlistenFn> {
  return listen<T>(event, (e) => handler(e.payload));
}
