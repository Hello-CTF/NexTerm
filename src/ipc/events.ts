// 事件订阅（§6.3）：Go → 前端事件统一走这里的三态适配。
import { DEMO, WEB, subscribe } from "../demo";
import {
  newBinaryChannel,
  newJsonChannel,
  onChannelReopen as onWsChannelReopen,
  disposeChannel as disposeWsChannel,
  subscribeEvent,
} from "./webTransport";
import {
  disposeWailsChannel,
  listenWailsEvent,
  newWailsChannel,
  type WailsChannel,
} from "./wails";

export type UnlistenFn = () => void;

export interface IpcChannel<T = unknown> {
  onmessage: (message: T) => void;
  toJSON(): string | null;
}

export const EVENTS = {
  sessionStatus: "session://status",
  terminalExit: "terminal://exit",
  terminalThrottled: "terminal://throttled",
  /**
   * 终端控制权 / 观看人数 / 进程结束状态变化。
   *
   * 与 Go 侧 `events::TERMINAL_CONTROL` 必须一致。**全局广播**：一次事件发到所有
   * 终端，各端按 `payload.tabId` 自己过滤；所以订阅方不能假定"这条事件是我这个标签的"。
   */
  terminalControl: "terminal://control",
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

/**
 * `terminal://control` 的 payload（camelCase，与 Go `TerminalControlPayload` 对齐）。
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

function demoChannel<T>(): IpcChannel<T> {
  return {
    onmessage: (_msg: T) => undefined,
    toJSON() {
      return null;
    },
  };
}

/** 终端二进制通道：接收 PTY 原始字节。 */
export function createBinaryChannel(
  onBytes: (data: Uint8Array) => void,
): IpcChannel<unknown> {
  const channel = DEMO
    ? demoChannel<unknown>()
    : WEB
      ? newBinaryChannel()
      : newWailsChannel();
  channel.onmessage = (raw) => decodeBytes(raw, onBytes);
  return channel;
}

/** 字符串 id 让 desktop / web 的 detach 都只摘掉当前订阅。 */
export function channelIdOf(ch: unknown): string | undefined {
  const id = (ch as { id?: unknown } | null)?.id;
  return typeof id === "string" ? id : undefined;
}

/** Web WS 重连通知；Wails 与 demo 没有这一生命周期。 */
export function onChannelReopen(ch: unknown, cb: () => void): () => void {
  const id = channelIdOf(ch);
  if (!WEB || id === undefined) return () => {};
  return onWsChannelReopen(id, cb);
}

/** 幂等关闭当前通道；必须在终端 detach / AI 终态之后调用。 */
export function disposeChannel(ch: unknown): void {
  const id = channelIdOf(ch);
  if (id === undefined || DEMO) return;
  if (WEB) {
    disposeWsChannel(id);
    return;
  }
  disposeWailsChannel(ch as WailsChannel);
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
export function createAiChannel(
  onEvent: (event: Record<string, unknown>) => void,
): IpcChannel<unknown> {
  const push = (raw: unknown) => {
    if (raw && typeof raw === "object") {
      onEvent(raw as Record<string, unknown>);
    } else if (typeof raw === "string") {
      try {
        onEvent(JSON.parse(raw) as Record<string, unknown>);
      } catch {
        onEvent({ type: "delta", text: raw });
      }
    }
  };
  const channel = DEMO
    ? demoChannel<unknown>()
    : WEB
      ? newJsonChannel()
      : newWailsChannel();
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
    return Promise.resolve(off);
  }
  return listenWailsEvent(event, handler);
}
