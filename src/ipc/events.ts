import { DEMO, WEB, subscribe } from "../demo";
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
  dockerStats: "docker://stats",
  aiEvent: "ai://event",
  appError: "app://error",
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

function demoChannel<T>(): IpcChannel<T> {
  return {
    onmessage: (_msg: T) => undefined,
    toJSON() {
      return null;
    },
  };
}

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
      ? newBinaryChannel()
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
