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
  connectProgress: "session://connect-progress",
  helperProgress: "session://helper-progress",
  terminalExit: "terminal://exit",
  terminalThrottled: "terminal://throttled",
  terminalControl: "terminal://control",
  fsProgress: "fs://progress",
  updateProgress: "update://progress",
  dockerStats: "docker://stats",
  aiEvent: "ai://event",
  aiNotification: "ai://notification",
  appError: "app://error",
  deepLink: "app://deep-link",
  deviceStatus: "device://status",
  filesDropped: "files://dropped",
  closeConfirm: "desktop://close-confirm",
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
  title?: string;
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

// Aligned with Go ipc.ConnectProgressEvent; hop 0 is the target host, 1..hops is the Nth jump host.
export interface ConnectProgressEvent {
  assetId: string;
  stage: string;
  hop: number;
  hops: number;
  host: string;
  port: number;
}

// Progress emitted while the remote persistent-terminal helper is inspected, uploaded, or started.
// sessionId and assetId are both included so a late event cannot update another terminal view.
export interface HelperProgressEvent {
  sessionId: string;
  assetId: string;
  step: number;
  steps: number;
  stage: string;
  transferred: number;
  total: number;
  percent: number;
  done: boolean;
  error?: string;
}

// Aligned with Go update.Progress; phase distinguishes the download and install stages.
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

// In-app notification pushed by the AI notify tool; ownerId is the session owner, the server filters by identity, the desktop delivers locally.
export interface AINotificationEvent {
  ownerId: string;
  text: string;
}

// Device control channel online/offline push; carries only the device ID and online state, visibility is still governed by
// the owner/super-admin filter of /fleet/devices; views ignore device IDs not in the list.
export interface DeviceStatusEvent {
  deviceId: string;
  online: boolean;
}

// Desktop native OS file drop, bridged by the desktop main from the wails WindowFilesDropped
// event: real local paths with dir classification plus webview-relative drop coordinates.
export interface NativeDroppedPath {
  path: string;
  dir: boolean;
}

export interface NativeFilesDroppedEvent {
  paths: NativeDroppedPath[];
  x: number;
  y: number;
}

export interface StreamAnchorControl {
  type: string;
  epoch: string;
  streamSeq: number;
  controlBytes?: number;
  expired?: boolean;
}

export const STREAM_ANCHOR_TYPE = "nexterm.stream-anchor";

function isStreamAnchor(raw: unknown): raw is StreamAnchorControl {
  return (
    typeof raw === "object" &&
    raw !== null &&
    (raw as { type?: unknown }).type === STREAM_ANCHOR_TYPE &&
    typeof (raw as { epoch?: unknown }).epoch === "string" &&
    typeof (raw as { streamSeq?: unknown }).streamSeq === "number"
  );
}

export function createBinaryChannel(
  onBytes: (data: Uint8Array) => void,
  onControl?: (control: StreamAnchorControl) => void,
): IpcChannel<unknown> {
  const channel = WEB ? newBinaryChannel() : newWailsChannel();
  channel.onmessage = (raw) => {
    if (isStreamAnchor(raw)) {
      onControl?.(raw);
      return;
    }
    decodeBytes(raw, onBytes);
  };
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
