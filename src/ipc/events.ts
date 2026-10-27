import { noteEvent } from "./probeHooks";
import { WEB } from "./env";
import {
  newBinaryChannel,
  onChannelReopen as onWsChannelReopen,
  disposeChannel as disposeWsChannel,
  subscribeEvent,
  sendChannelInput as sendWsChannelInput,
} from "./webTransport";
import {
  disposeWailsChannel,
  listenWailsEvent,
  newWailsChannel,
  type WailsChannel,
} from "./wails";
import type { ipcEventTopics } from "./generated/events";

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
  terminalAttention: "terminal://attention",
  fsProgress: "fs://progress",
  updateProgress: "update://progress",
  dockerStats: "docker://stats",
  aiEvent: "ai://event",
  aiNotification: "ai://notification",
  aiWatchToolData: "ai://watch-tool-data",
  appError: "app://error",
  deepLink: "app://deep-link",
  notificationClick: "app://notification-click",
  deviceStatus: "device://status",
  filesDropped: "files://dropped",
  layoutChanged: "layout://changed",
  inventoryChanged: "inventory://changed",
  localHelperStatus: "local://helper-status",
  syncStatus: "sync://status",
  syncRoundComplete: "sync://round-complete",
} as const satisfies typeof ipcEventTopics;

export type { SessionStatusEvent, TerminalExitEvent } from "./types";
export type { AttentionEvent as TerminalAttentionEvent } from "./generated/types";

// A click on a system notification the desktop showed; tabId is the backend tab it was about.
export interface NotificationClickEvent {
  id: string;
  tabId?: string;
}

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
  titleCustom?: boolean;
  titleChangedAt?: number;
  // Durable tabs: helper offset of the first rendered byte (earlier output is
  // readable with terminal.historyRead), the oldest kept offset, and whether
  // the helper's history limit dropped output.
  historyFrom?: number;
  historyBase?: number;
  historyTruncated?: boolean;
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
  operationId?: string;
  attempt?: number;
  attempts?: number;
  deadline?: number;
  step: number;
  steps: number;
  stage: string;
  cause?: string;
  path?: string;
  transport?: string;
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
  // The channel was disconnected because it could not keep up; its view
  // resumes from its accepted offset.
  resync?: boolean;
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
  // The attachment incarnation this anchor was sent for: the client adopts
  // it as its current attachment identity, so the ordered stream itself
  // carries the incarnation even before the attach RPC response arrives.
  attachmentId?: number;
  // Output-class spans (by stream sequence) of the bytes this stream will
  // deliver; later spans arrive as stream-class frames.
  classes?: StreamClassControl[];
  // Durable tabs, full replays: helper offset of the first helper byte this
  // view renders; earlier output is read with terminal.historyRead.
  historyFrom?: number;
}

export const STREAM_ANCHOR_TYPE = "nexterm.stream-anchor";

// Announces that the next `bytes` stream bytes are replayed output, for
// streams without anchors (a resumed docker exec delivering its backlog).
export interface StreamReplayControl {
  type: string;
  bytes: number;
}

export const STREAM_REPLAY_TYPE = "nexterm.stream-replay";

// Classifies the stream sequence range [from, to) ahead of its bytes:
// "replay" renders without side effects, "answered" without automatic replies.
export interface StreamClassControl {
  type?: string;
  from: number;
  to: number;
  kind: string;
}

export const STREAM_CLASS_TYPE = "nexterm.stream-class";

function isStreamClass(raw: unknown): raw is StreamClassControl {
  return (
    typeof raw === "object" &&
    raw !== null &&
    (raw as { type?: unknown }).type === STREAM_CLASS_TYPE &&
    typeof (raw as { from?: unknown }).from === "number" &&
    typeof (raw as { to?: unknown }).to === "number" &&
    typeof (raw as { kind?: unknown }).kind === "string"
  );
}

// Tells this view whether it is the device that most recently typed into the
// tab: only that device writes live OSC 52 clipboard requests. Every anchor
// resets the view to active.
export interface InputOwnerControl {
  type: string;
  active: boolean;
}

export const INPUT_OWNER_TYPE = "nexterm.input-owner";

function isInputOwner(raw: unknown): raw is InputOwnerControl {
  return (
    typeof raw === "object" &&
    raw !== null &&
    (raw as { type?: unknown }).type === INPUT_OWNER_TYPE &&
    typeof (raw as { active?: unknown }).active === "boolean"
  );
}

function isStreamReplay(raw: unknown): raw is StreamReplayControl {
  return (
    typeof raw === "object" &&
    raw !== null &&
    (raw as { type?: unknown }).type === STREAM_REPLAY_TYPE &&
    typeof (raw as { bytes?: unknown }).bytes === "number"
  );
}

function isStreamAnchor(raw: unknown): raw is StreamAnchorControl {
  return (
    typeof raw === "object" &&
    raw !== null &&
    (raw as { type?: unknown }).type === STREAM_ANCHOR_TYPE &&
    typeof (raw as { epoch?: unknown }).epoch === "string" &&
    typeof (raw as { streamSeq?: unknown }).streamSeq === "number"
  );
}

export const DESKTOP_STREAM_CLOSED_TYPE = "nexterm.desktop-stream-closed";

const desktopReopenSubs = new Map<string, Set<() => void>>();

function isDesktopStreamClosed(raw: unknown): boolean {
  return (
    typeof raw === "object" &&
    raw !== null &&
    (raw as { type?: unknown }).type === DESKTOP_STREAM_CLOSED_TYPE
  );
}

function notifyDesktopChannelReopen(channelId: string) {
  const subs = desktopReopenSubs.get(channelId);
  if (!subs) return;
  for (const cb of [...subs]) {
    try {
      cb();
    } catch {
    }
  }
}

function handleDesktopStreamClosed(channel: unknown, raw: unknown): boolean {
  if (WEB || !isDesktopStreamClosed(raw)) return false;
  const id = channelIdOf(channel);
  if (id !== undefined) notifyDesktopChannelReopen(id);
  return true;
}

export function createBinaryChannel(
  onBytes: (data: Uint8Array) => void,
  onControl?: (control: StreamAnchorControl) => void,
  onReplay?: (control: StreamReplayControl) => void,
  onClass?: (control: StreamClassControl) => void,
  onInputOwner?: (control: InputOwnerControl) => void,
): IpcChannel<unknown> {
  const channel = WEB ? newBinaryChannel() : newWailsChannel();
  channel.onmessage = (raw) => {
    if (handleDesktopStreamClosed(channel, raw)) return;
    if (isStreamAnchor(raw)) {
      onControl?.(raw);
      return;
    }
    if (isStreamReplay(raw)) {
      onReplay?.(raw);
      return;
    }
    if (isStreamClass(raw)) {
      onClass?.(raw);
      return;
    }
    if (isInputOwner(raw)) {
      onInputOwner?.(raw);
      return;
    }
    decodeBytes(raw, onBytes);
  };
  return channel;
}

/** Terminal input: over the web channel socket when negotiated, otherwise through `http` (always on desktop). */
export function sendChannelInput(
  ch: unknown,
  tabId: string,
  data: Uint8Array,
  http: (data: Uint8Array) => Promise<unknown>,
): void {
  const id = channelIdOf(ch);
  if (!WEB) {
    sendDesktopInput(tabId, data, http);
    return;
  }
  if (id === undefined) {
    void http(data).catch(() => undefined);
    return;
  }
  sendWsChannelInput(id, tabId, data, http);
}

const DESKTOP_INPUT_MAX_PENDING = 4 * 1024 * 1024;
const desktopInput = new Map<string, Uint8Array[]>();

/**
 * Desktop input keeps one write in flight per tab. Each write waits for the host to accept the bytes, so mouse
 * reports from a full-screen app (wheel, drag selection) would otherwise queue one round trip each; bytes that
 * arrive meanwhile go out merged in the next write, in order.
 */
function sendDesktopInput(tabId: string, data: Uint8Array, http: (data: Uint8Array) => Promise<unknown>): void {
  const queued = desktopInput.get(tabId);
  if (queued) {
    const pending = queued.reduce((n, chunk) => n + chunk.length, 0);
    if (pending + data.length > DESKTOP_INPUT_MAX_PENDING) {
      console.warn(`terminal input dropped: ${data.length} bytes exceed the pending input cap`);
      return;
    }
    queued.push(data);
    return;
  }
  const chunks: Uint8Array[] = [];
  desktopInput.set(tabId, chunks);
  void (async () => {
    let next: Uint8Array | null = data;
    while (next) {
      await http(next).catch(() => undefined);
      next = chunks.length > 0 ? mergeInput(chunks.splice(0)) : null;
    }
    desktopInput.delete(tabId);
  })();
}

function mergeInput(chunks: Uint8Array[]): Uint8Array {
  if (chunks.length === 1) return chunks[0];
  const merged = new Uint8Array(chunks.reduce((n, chunk) => n + chunk.length, 0));
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.length;
  }
  return merged;
}

export function channelIdOf(ch: unknown): string | undefined {
  const id = (ch as { id?: unknown } | null)?.id;
  return typeof id === "string" ? id : undefined;
}

export function onChannelReopen(ch: unknown, cb: () => void): () => void {
  const id = channelIdOf(ch);
  if (id === undefined) return () => {};
  if (WEB) return onWsChannelReopen(id, cb);
  let subs = desktopReopenSubs.get(id);
  if (!subs) {
    subs = new Set();
    desktopReopenSubs.set(id, subs);
  }
  subs.add(cb);
  return () => {
    const current = desktopReopenSubs.get(id);
    if (!current) return;
    current.delete(cb);
    if (current.size === 0) desktopReopenSubs.delete(id);
  };
}

export function disposeChannel(ch: unknown): void {
  const id = channelIdOf(ch);
  if (id === undefined) return;
  if (WEB) {
    disposeWsChannel(id);
    return;
  }
  desktopReopenSubs.delete(id);
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

export function createJsonChannel(
  onEvent: (event: Record<string, unknown>) => void,
): IpcChannel<unknown> {
  const push = (raw: unknown) => {
    if (handleDesktopStreamClosed(channel, raw)) return;
    if (raw && typeof raw === "object") {
      onEvent(raw as Record<string, unknown>);
    }
  };
  const channel = WEB ? newBinaryChannel() : newWailsChannel();
  channel.onmessage = push;
  return channel;
}

export function createAiChannel(
  onEvent: (event: Record<string, unknown>) => void,
): IpcChannel<unknown> {
  return createJsonChannel(onEvent);
}

export function listenEvent<T>(
  event: string,
  rawHandler: (payload: T) => void,
): Promise<UnlistenFn> {
  const handler = (payload: T) => {
    noteEvent(event);
    rawHandler(payload);
  };
  if (WEB) {
    const off = subscribeEvent(event, (payload) => handler(payload as T));
    return Promise.resolve(off);
  }
  return listenWailsEvent(event, handler);
}
