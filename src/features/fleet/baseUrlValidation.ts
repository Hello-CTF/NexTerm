// Pre-validation of base URLs, same rules as the server-side fleetserver.normalizeBaseURL:
// explicit http/https, hostname required, no user info/query/fragment;
// plain http is limited to local/private addresses, otherwise insecure must be checked.
// The client intercepts first; the server still does the final normalization (dedup/trailing slash/lowercase scheme).

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

// Returns the normalized URL or an error message; on success the URL has its trailing slash removed and scheme lowercased.
export function validateBaseURL(raw: string, insecure: boolean): BaseURLValidation {
  const trimmed = raw.trim();
  if (!trimmed) return { url: "", error: "接入地址不能为空" };
  if (trimmed.length > MAX_BASE_URL_LENGTH) {
    return { url: "", error: "接入地址不能超过 2048 个字符" };
  }
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return { url: "", error: "接入地址格式无效" };
  }
  const scheme = parsed.protocol.replace(/:$/, "").toLowerCase();
  if ((scheme !== "http" && scheme !== "https") || !parsed.hostname) {
    return { url: "", error: "接入地址必须以 http:// 或 https:// 开头，并包含主机名" };
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    return { url: "", error: "接入地址不能包含用户信息、查询参数或片段" };
  }
  if (scheme === "http" && !insecure && !isLocalOrPrivate(parsed.hostname)) {
    return { url: "", error: "公网地址必须使用 HTTPS；设备凭据不能通过明文 HTTP 传输" };
  }
  // JS URL normalizes an empty path to "/"; here the trailing slash is manually stripped per Go semantics.
  const path = parsed.pathname.replace(/\/+$/, "");
  return { url: `${scheme}://${parsed.host}${path}`, error: null };
}
