import { wsUrl } from "./env";

export type PublicSharePermission = "read" | "read_write";

export interface PublicShareReady {
  sessionId: string;
  permission: PublicSharePermission;
  expiresAt: number;
}

export interface PublicShareFailure {
  code: string;
  message: string;
}

export interface PublicShareCloseInfo {
  code: number;
  reason: string;
  wasClean: boolean;
}

export interface PublicShareHandlers {
  onReady: (ready: PublicShareReady) => void;
  onOutput: (bytes: Uint8Array) => void;
  onError: (failure: PublicShareFailure) => void;
  onClose: (info: PublicShareCloseInfo) => void;
}

export function publicShareWsUrl(token: string): string {
  return wsUrl(`/share/public/${encodeURIComponent(token)}`);
}

// 输入方向只对显式 read_write 放行; 合同里任何其他值一律按只读处理。
function parsePermission(value: unknown): PublicSharePermission {
  return value === "read_write" ? "read_write" : "read";
}

export class PublicShareConnection {
  private readonly socket: WebSocket;

  constructor(token: string, private readonly handlers: PublicShareHandlers) {
    const socket = new WebSocket(publicShareWsUrl(token));
    socket.binaryType = "arraybuffer";
    socket.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        this.dispatchText(event.data);
      } else {
        this.handlers.onOutput(new Uint8Array(event.data as ArrayBuffer));
      }
    });
    socket.addEventListener("close", (event) => {
      this.handlers.onClose({ code: event.code, reason: event.reason, wasClean: event.wasClean });
    });
    this.socket = socket;
  }

  sendInput(data: string | Uint8Array): void {
    if (this.socket.readyState !== WebSocket.OPEN) return;
    this.socket.send(typeof data === "string" ? new TextEncoder().encode(data) : data);
  }

  close(): void {
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
      this.handlers.onReady({
        sessionId: typeof raw.session_id === "string" ? raw.session_id : "",
        permission: parsePermission(raw.permission),
        expiresAt: typeof raw.expires_at === "number" ? raw.expires_at : 0,
      });
    } else if (raw.type === "error") {
      this.handlers.onError({
        code: typeof raw.code === "string" ? raw.code : "internal",
        message: typeof raw.message === "string" ? raw.message : "",
      });
    }
  }
}
