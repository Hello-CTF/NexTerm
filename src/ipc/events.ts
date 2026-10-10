import { WEB } from "./env";
import {
  newBinaryChannel,
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
  terminalControl: "terminal://control",
  fsProgress: "fs://progress",
  updateProgress: "update://progress",
  dockerStats: "docker://stats",
  aiEvent: "ai://event",
  appError: "app://error",
  deepLink: "app://deep-link",
  deviceStatus: "device://status",
} as const;

export type { SessionStatusEvent, TerminalExitEvent } from "./types";

export interface TerminalControlEvent {
  tabId: string;
  controller: string | null;
  subscribers: number;
  viewers: number;
  exited: boolean;
  cols: number;
  rows: number;
  gridRevision: number;
  version: number;
  cwd?: string;
  durable?: boolean;
}

export class EventVersionGate {
  private readonly highWater = new Map<string, number>();

  accept(key: string, version: number): boolean {
    if (!key || !Number.isFinite(version) || version <= 0) return false;
    const seen = this.highWater.get(key) ?? 0;
    if (version <= seen) return false;
    this.highWater.set(key, version);
    return true;
  }
}

export interface FsProgressEvent {
  taskId: string;
  transferred: number;
  total: number;
  done: boolean;
  error?: string;
}

// 与 Go update.Progress 对齐; phase 区分 download 与 install 阶段。
export interface UpdateProgressEvent {
  taskId: string;
  phase?: string;
  transferred: number;
  total: number;
  done: boolean;
  error?: string;
}

export interface TerminalThrottledEvent {
  tabId: string;
  channelId: string;
  inflightBytes: number;
  recovered?: boolean;
  version: number;
}

export interface AppErrorEvent {
  code: string;
  message: string;
}

// 设备控制通道上下线推送; 只含设备 ID 与在线状态, 可见范围仍由
// /fleet/devices 的 owner/超管过滤决定, 视图对不在清单内的设备 ID 直接忽略。
export interface DeviceStatusEvent {
  deviceId: string;
  online: boolean;
}

export function createBinaryChannel(
  onBytes: (data: Uint8Array) => void,
): IpcChannel<unknown> {
  const channel = WEB ? newBinaryChannel() : newWailsChannel();
  channel.onmessage = (raw) => decodeBytes(raw, onBytes);
  return channel;
}

export function channelIdOf(ch: unknown): string | undefined {
  const id = (ch as { id?: unknown } | null)?.id;
  return typeof id === "string" ? id : undefined;
}

export function onChannelReopen(ch: unknown, cb: () => void): () => void {
  const id = channelIdOf(ch);
  if (!WEB || id === undefined) return () => {};
  return onWsChannelReopen(id, cb);
}

export function disposeChannel(ch: unknown): void {
  const id = channelIdOf(ch);
  if (id === undefined) return;
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

export function createAiChannel(
  onEvent: (event: Record<string, unknown>) => void,
): IpcChannel<unknown> {
  const push = (raw: unknown) => {
    if (raw && typeof raw === "object") {
      onEvent(raw as Record<string, unknown>);
    }
  };
  const channel = WEB ? newBinaryChannel() : newWailsChannel();
  channel.onmessage = push;
  return channel;
}

export function listenEvent<T>(
  event: string,
  handler: (payload: T) => void,
): Promise<UnlistenFn> {
  if (WEB) {
    const off = subscribeEvent(event, (payload) => handler(payload as T));
    return Promise.resolve(off);
  }
  return listenWailsEvent(event, handler);
}
