// 公开分享链接创建 (FLEET168): 从已 attached 的设备远程终端经真实
// POST /share/links 创建绑定该会话的公开链接。合同对齐
// internal/fleet/server/sharing_http.go: 创建响应是唯一携带一次性 token
// 的入口 (列表/吊销响应不含 token), token 只用于当场拼公开 URL, 不写日志、
// 不持久化、不进 web storage。默认只读 (write=false); ttl_ms 总是显式携带
// 且必须落在服务端 minShareTTL(1 分钟)-maxShareTTL(30 天) 边界内。

import { request } from "../../ipc/authApi";
import { apiBase } from "../../ipc/env";
import type { ShareLinkView } from "../../ipc/sharingApi";

// ShareLinkCreateResponse 是创建响应的完整形状: shareLinkView 字段 +
// 一次性 token。除创建外的任何响应都不含 token。
export interface ShareLinkCreateResponse extends ShareLinkView {
  token: string;
}

export function createShareLink(input: {
  deviceId: string;
  sessionId: string;
  write: boolean;
  ttlMs: number;
}): Promise<ShareLinkCreateResponse> {
  return request<ShareLinkCreateResponse>(
    "POST",
    "/share/links",
    { device_id: input.deviceId, session_id: input.sessionId, write: input.write, ttl_ms: input.ttlMs },
    { csrf: true },
  );
}

// sharePublicUrl 拼公开观看地址: GET /share/public/{token} 由服务器直接
// 服务 SPA 入口 (匿名可开, token URL 一律 no-store)。取 API 源 (跨源 ?api=
// 部署) 或当前页面源, 保证复制出去的是绝对地址。token 只做这一次 URL 拼接。
export function sharePublicUrl(token: string): string {
  const base = apiBase() || window.location.origin;
  return `${base}/share/public/${token}`;
}
