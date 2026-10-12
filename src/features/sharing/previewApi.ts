// 远程预览 (预览链接): 为服务端托管的终端标签页创建匿名可开的预览, 只读或
// 可写由创建时的 write 决定 (链接视图 permission: "read"|"read_write")。合同
// 对齐 internal/server/preview_http.go: 创建响应是唯一携带一次性 token 的
// 入口 (列表/吊销响应不含 token), token 只用于当场拼公开 URL, 不写日志、不
// 持久化、不进 web storage。ttl_ms 总是显式携带且必须落在服务端
// minShareTTL(1 分钟)-maxShareTTL(30 天) 边界内。

import { request } from "../../ipc/authApi";
import { apiBase, wsUrl } from "../../ipc/env";

export type PreviewPermission = "read" | "read_write";

export interface PreviewLinkView {
  id: string;
  owner_id: string;
  session_id: string;
  // WEB 服务端总是下发; 桌面远程中继的链接视图 (RemotePreviewLinkView) 没有该
  // 字段, 消费方对缺失/未知值一律按只读处理。
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

// previewPublicUrl 拼公开观看地址: GET /share/preview/{token} 由服务器直接
// 服务 SPA 入口 (匿名可开, token URL 一律 no-store)。token 只做这一次 URL 拼接。
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

// PreviewSnapshot 是订阅通道的首帧: 当前屏幕的纯文本行与光标位置, 不含滚动
// 历史与字符属性。
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

// 输入方向只对显式 read_write 放行; 合同里任何其他值一律按只读处理。
function parsePermission(value: unknown): PreviewPermission {
  return value === "read_write" ? "read_write" : "read";
}

// PreviewConnection 是预览的 WS 客户端: 下行是 ready/snapshot/control/error
// 文本帧与输出二进制帧, 上行输入是二进制帧 (sendInput), 是否放行由 ready 的
// permission 与服务端 control 帧的租约状态决定, 协议层两个方向都存在。
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
