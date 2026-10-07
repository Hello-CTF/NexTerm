// 接入地址的前置校验, 与服务端 fleetserver.normalizeBaseURL 同一套规则:
// 显式 http/https、必须含主机名、不能带用户信息/查询参数/片段;
// 明文 http 仅限本机/内网地址, 否则必须勾选 insecure。
// 客户端先拦一道, 服务端仍会做最终规范化 (去重/去尾斜杠/小写 scheme)。

export const MAX_BASE_URLS = 8;
export const MAX_BASE_URL_LENGTH = 2048;

function isLocalOrPrivate(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/\.$/, "");
  if (host === "localhost" || host.endsWith(".localhost") || host.endsWith(".local")) return true;
  const ipv4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (ipv4) {
    const [a, b] = [Number(ipv4[1]), Number(ipv4[2])];
    if (a === 127 || a === 10) return true;
    if (a === 172 && b >= 16 && b <= 31) return true;
    if (a === 192 && b === 168) return true;
    if (a === 169 && b === 254) return true;
    return false;
  }
  const ipv6 = /^[0-9a-f:]+$/.test(host) && host.includes(":");
  if (ipv6) {
    if (host === "::1" || host === "0:0:0:0:0:0:0:1") return true;
    if (host.startsWith("fc") || host.startsWith("fd")) return true;
    if (host.startsWith("fe80")) return true;
  }
  return false;
}

export interface BaseURLValidation {
  url: string;
  error: string | null;
}

// 返回规范化后的地址或错误信息; 通过时 url 已去尾斜杠、scheme 小写。
export function validateBaseURL(raw: string, insecure: boolean): BaseURLValidation {
  const trimmed = raw.trim();
  if (!trimmed) return { url: "", error: "接入地址不能为空" };
  if (trimmed.length > MAX_BASE_URL_LENGTH) {
    return { url: "", error: `接入地址长度需在 1-${MAX_BASE_URL_LENGTH} 之间` };
  }
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return { url: "", error: "接入地址 URL 不合法" };
  }
  const scheme = parsed.protocol.replace(/:$/, "").toLowerCase();
  if ((scheme !== "http" && scheme !== "https") || !parsed.hostname) {
    return { url: "", error: "接入地址必须显式使用 http:// 或 https://, 并包含主机名" };
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    return { url: "", error: "接入地址不能包含用户信息、查询参数或片段" };
  }
  if (scheme === "http" && !insecure && !isLocalOrPrivate(parsed.hostname)) {
    return { url: "", error: "公网地址必须使用 HTTPS; 设备凭证不能通过明文 HTTP 传输" };
  }
  // JS URL 会把空 path 归一化成 "/", 这里按 Go 的语义手动拼回去尾斜杠的结果。
  const path = parsed.pathname.replace(/\/+$/, "");
  return { url: `${scheme}://${parsed.host}${path}`, error: null };
}
