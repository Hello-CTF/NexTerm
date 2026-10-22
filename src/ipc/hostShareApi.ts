import { apiBase, WEB, wsUrl } from "./env";
import { currentAuthSessionEpoch } from "./authSessionEpoch";
import { channelIdOf, createAiChannel, disposeChannel, type IpcChannel } from "./events";
import { remoteHostShareApi } from "./remoteApi";
import type {
  PublicShareCloseInfo,
  PublicShareFailure,
  PublicShareHandlers,
  PublicShareReady,
} from "./publicShareApi";

export type HostShareReady = PublicShareReady;
export type HostShareFailure = PublicShareFailure;
export type HostShareCloseInfo = PublicShareCloseInfo;
export type HostShareHandlers = PublicShareHandlers;

export interface HostShareConnection {
  sendInput: (data: string | Uint8Array) => void;
  close: () => void;
}

export function hostShareWsUrl(deviceId: string): string {
  return wsUrl(`/share/devices/${encodeURIComponent(deviceId)}/terminal`);
}

export function normalizeHostShareServerUrl(raw: string): string {
  const trimmed = raw.trim();
  try {
    const url = new URL(trimmed || window.location.origin, window.location.origin);
    url.username = "";
    url.password = "";
    url.search = "";
    url.hash = "";
    url.pathname = url.pathname.replace(/\/+$/, "");
    return url.toString().replace(/\/+$/, "");
  } catch {
    return trimmed.replace(/\/+$/, "");
  }
}

export function hostShareIdentityScope(serverUrl: string, userId: string, generation: string): string {
  return JSON.stringify([normalizeHostShareServerUrl(serverUrl), userId, generation]);
}

export function browserHostShareIdentityScope(userId: string): string {
  return hostShareIdentityScope(apiBase() || window.location.origin, userId, String(currentAuthSessionEpoch()));
}

function permission(value: unknown): "read" | "read_write" {
  return value === "read_write" ? "read_write" : "read";
}

function failure(error: unknown): HostShareFailure {
  const raw = error && typeof error === "object" ? error as Record<string, unknown> : null;
  return {
    code: typeof raw?.code === "string" ? raw.code : "disconnected",
    message: typeof raw?.message === "string" ? raw.message : "共享终端连接失败",
  };
}

class BrowserHostShareConnection implements HostShareConnection {
  private readonly socket: WebSocket;
  private writable = false;
  private closeSent = false;

  constructor(deviceId: string, private readonly handlers: HostShareHandlers) {
    const socket = new WebSocket(hostShareWsUrl(deviceId));
    socket.binaryType = "arraybuffer";
    socket.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        this.dispatchText(event.data);
      } else {
        this.handlers.onOutput(new Uint8Array(event.data as ArrayBuffer));
      }
    });
    socket.addEventListener("close", (event) => {
      this.writable = false;
      this.handlers.onClose({ code: event.code, reason: event.reason, wasClean: event.wasClean });
    });
    this.socket = socket;
  }

  sendInput(data: string | Uint8Array): void {
    if (!this.writable || this.socket.readyState !== WebSocket.OPEN) return;
    this.socket.send(typeof data === "string" ? new TextEncoder().encode(data) : data);
  }

  close(): void {
    this.writable = false;
    if (this.closeSent) return;
    this.closeSent = true;
    try {
      this.socket.close();
    } catch {
    }
  }

  private dispatchText(payload: string): void {
    let frame: unknown;
    try {
      frame = JSON.parse(payload);
    } catch {
      return;
    }
    if (typeof frame !== "object" || frame === null) return;
    const raw = frame as Record<string, unknown>;
    if (raw.type === "ready") {
      const ready = parseReady(raw);
      this.writable = ready.permission === "read_write";
      this.handlers.onReady(ready);
    } else if (raw.type === "error") {
      this.handlers.onError(parseError(raw));
    }
  }
}

class DesktopHostShareConnection implements HostShareConnection {
  private readonly channel: IpcChannel<unknown>;
  private readonly streamId: string;
  private readonly openPromise: Promise<boolean>;
  private inputQueue: Promise<void> = Promise.resolve();
  private writable = false;
  private opened = false;
  private closed = false;
  private closeSent = false;
  private closeNotified = false;

  constructor(deviceId: string, private readonly handlers: HostShareHandlers) {
    this.channel = createAiChannel((raw) => this.dispatch(raw));
    const streamId = channelIdOf(this.channel);
    if (!streamId) throw new Error("桌面共享终端通道不可用");
    this.streamId = streamId;
    this.openPromise = remoteHostShareApi.open(deviceId, streamId, this.channel).then(
      () => {
        this.opened = !this.closed;
        if (this.closed) void remoteHostShareApi.close(this.streamId).catch(() => undefined);
        return this.opened;
      },
      (error: unknown) => {
        this.fail(error);
        return false;
      },
    );
  }

  sendInput(data: string | Uint8Array): void {
    if (!this.writable || this.closed) return;
    const bytes = typeof data === "string" ? new TextEncoder().encode(data) : new Uint8Array(data);
    this.inputQueue = this.inputQueue.then(async () => {
      const opened = await this.openPromise;
      if (!opened || !this.opened || !this.writable || this.closed) return;
      await remoteHostShareApi.input(this.streamId, bytes);
    }).catch((error: unknown) => this.fail(error));
  }

  close(): void {
    this.closed = true;
    this.writable = false;
    disposeChannel(this.channel);
    if (this.closeSent) return;
    this.closeSent = true;
    void remoteHostShareApi.close(this.streamId).catch(() => undefined);
    void this.openPromise.then(async () => {
      await remoteHostShareApi.close(this.streamId).catch(() => undefined);
    }).catch(() => undefined);
  }

  private dispatch(raw: unknown): void {
    if (raw instanceof ArrayBuffer) {
      this.handlers.onOutput(new Uint8Array(raw));
      return;
    }
    if (raw instanceof Uint8Array || Array.isArray(raw)) {
      this.handlers.onOutput(new Uint8Array(raw));
      return;
    }
    if (typeof raw !== "object" || raw === null) return;
    const frame = raw as Record<string, unknown>;
    if (frame.type === "ready") {
      const ready = parseReady(frame);
      this.writable = ready.permission === "read_write";
      this.handlers.onReady(ready);
    } else if (frame.type === "error") {
      this.handlers.onError(parseError(frame));
    } else if (frame.type === "output" && typeof frame.data === "string") {
      try {
        const binary = atob(frame.data);
        const bytes = new Uint8Array(binary.length);
        for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
        this.handlers.onOutput(bytes);
      } catch {
      }
    } else if (frame.type === "close") {
      this.notifyClose({
        code: typeof frame.code === "number" ? frame.code : 1006,
        reason: typeof frame.reason === "string" ? frame.reason : "",
        wasClean: frame.wasClean === true,
      });
    }
  }

  private fail(error: unknown): void {
    if (this.closed) return;
    const failure = failureFrom(error);
    this.handlers.onError(failure);
    this.notifyClose({ code: 1006, reason: failure.message, wasClean: false });
    this.close();
  }

  private notifyClose(info: HostShareCloseInfo): void {
    if (this.closeNotified) return;
    this.closeNotified = true;
    this.closed = true;
    this.writable = false;
    disposeChannel(this.channel);
    this.handlers.onClose(info);
  }
}

function parseReady(raw: Record<string, unknown>): HostShareReady {
  return {
    sessionId: typeof raw.session_id === "string" ? raw.session_id : "",
    permission: permission(raw.permission),
    expiresAt: typeof raw.expires_at === "number" ? raw.expires_at : 0,
  };
}

function parseError(raw: Record<string, unknown>): HostShareFailure {
  return {
    code: typeof raw.code === "string" ? raw.code : "internal",
    message: typeof raw.message === "string" ? raw.message : "",
  };
}

function failureFrom(error: unknown): HostShareFailure {
  return failure(error);
}

export function createHostShareConnection(deviceId: string, handlers: HostShareHandlers): HostShareConnection {
  return WEB
    ? new BrowserHostShareConnection(deviceId, handlers)
    : new DesktopHostShareConnection(deviceId, handlers);
}
