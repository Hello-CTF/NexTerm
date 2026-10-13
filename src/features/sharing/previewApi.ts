// Remote preview (preview links): creates anonymously openable previews of server-hosted terminal tabs; read-only or
// writable is decided by write at creation time (link view permission: "read"|"read_write"). Contract
// aligned with internal/server/preview_http.go: the create response is the only entry carrying the one-time token
// (list/revoke responses carry no token); the token is used only to assemble the public URL on the spot - never logged,
// persisted, or put into web storage. ttl_ms is always sent explicitly and must fall within the server-side
// minShareTTL(1 minute)-maxShareTTL(30 days) bounds.

import { request } from "../../ipc/authApi";
import { apiBase, wsUrl } from "../../ipc/env";

export type PreviewPermission = "read" | "read_write";

export interface PreviewLinkView {
  id: string;
  owner_id: string;
  session_id: string;
  // The WEB server always sends this; the desktop remote relay's link view (RemotePreviewLinkView) lacks the
  // field, and consumers treat missing/unknown values as read-only.
  permission?: PreviewPermission;
  created_at: number;
  expires_at: number;
  revoked_at?: number;
  last_accessed_at?: number;
}

export interface PreviewLinkCreateResponse extends PreviewLinkView {
  token: string;
}

export function createPreviewLink(input: { sessionId: string; ttlMs: number; write?: boolean }): Promise<PreviewLinkCreateResponse> {
  return request<PreviewLinkCreateResponse>(
    "POST",
    "/share/previews",
    { session_id: input.sessionId, ttl_ms: input.ttlMs, write: input.write ?? false },
    { csrf: true },
  );
}

export function listPreviewLinks(): Promise<{ links: PreviewLinkView[] }> {
  return request<{ links: PreviewLinkView[] }>("GET", "/share/previews");
}

export function revokePreviewLink(id: string): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>("POST", `/share/previews/${encodeURIComponent(id)}/revoke`, undefined, { csrf: true });
}

// previewPublicUrl assembles the public viewing address: GET /share/preview/{token} is served directly by the server
// as the SPA entry (anonymously openable, token URLs are always no-store). The token is used only for this one URL assembly.
export function previewPublicUrl(token: string): string {
  const base = apiBase() || window.location.origin;
  return `${base}/share/preview/${token}`;
}

export interface PreviewReady {
  sessionId: string;
  permission: PreviewPermission;
  expiresAt: number;
  leaseMs?: number;
}

export type PreviewControlState = "guest" | "owner" | "busy";

export interface PreviewControl {
  state: PreviewControlState;
  leaseUntil?: number;
}

// PreviewSnapshot is the first frame of the subscription channel: the current screen's plain text lines and cursor position, without scroll
// history or character attributes.
export interface PreviewSnapshot {
  lines: string[];
  cursorRow: number;
  cursorCol: number;
  cols: number;
  rows: number;
}

export interface PreviewFailure {
  code: string;
  message: string;
}

export interface PreviewCloseInfo {
  code: number;
  reason: string;
  wasClean: boolean;
}

export interface PreviewHandlers {
  onReady: (ready: PreviewReady) => void;
  onSnapshot: (snapshot: PreviewSnapshot) => void;
  onOutput: (bytes: Uint8Array) => void;
  onError: (failure: PreviewFailure) => void;
  onClose: (info: PreviewCloseInfo) => void;
  onControl?: (control: PreviewControl) => void;
}

export function previewWsUrl(token: string): string {
  return wsUrl(`/share/preview/${encodeURIComponent(token)}`);
}

// Input direction is allowed only for explicit read_write; any other value in the contract is treated as read-only.
function parsePermission(value: unknown): PreviewPermission {
  return value === "read_write" ? "read_write" : "read";
}

// PreviewConnection is the preview WS client: downstream carries ready/snapshot/control/error
// text frames and output binary frames; upstream input is binary frames (sendInput); whether input is allowed is decided by ready's
// permission and the server control frame's lease state; both directions exist at the protocol layer.
export class PreviewConnection {
  private readonly socket: WebSocket;

  constructor(token: string, private readonly handlers: PreviewHandlers) {
    const socket = new WebSocket(previewWsUrl(token));
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
        ...(typeof raw.lease_ms === "number" ? { leaseMs: raw.lease_ms } : {}),
      });
    } else if (raw.type === "snapshot") {
      this.handlers.onSnapshot({
        lines: Array.isArray(raw.lines) ? raw.lines.filter((line): line is string => typeof line === "string") : [],
        cursorRow: typeof raw.cursor_row === "number" ? raw.cursor_row : 0,
        cursorCol: typeof raw.cursor_col === "number" ? raw.cursor_col : 0,
        cols: typeof raw.cols === "number" ? raw.cols : 0,
        rows: typeof raw.rows === "number" ? raw.rows : 0,
      });
    } else if (raw.type === "control") {
      this.handlers.onControl?.({
        state: raw.state === "owner" || raw.state === "busy" ? raw.state : "guest",
        ...(typeof raw.lease_until === "number" ? { leaseUntil: raw.lease_until } : {}),
      });
    } else if (raw.type === "error") {
      this.handlers.onError({
        code: typeof raw.code === "string" ? raw.code : "internal",
        message: typeof raw.message === "string" ? raw.message : "",
      });
    }
  }
}
