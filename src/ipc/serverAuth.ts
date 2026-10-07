export const SERVER_TOKEN_HEADER = "X-NexTerm-Sync-Token";

const WS_AUTH_PROTOCOL = "nexterm";

// 与 src/ipc/authApi.ts 的 SESSION_EXPIRED_EVENT 保持一致:401 时通知账号门重新登录。
const SESSION_EXPIRED_EVENT = "nexterm:session-expired";

let serverToken: string | null = null;

export function getServerToken(): string | null {
  return serverToken;
}

export function setServerToken(token: string | null): void {
  const trimmed = token?.trim() ?? "";
  serverToken = trimmed === "" ? null : trimmed;
}

export function clearServerToken(): void {
  serverToken = null;
}

export function authHeaders(): Record<string, string> {
  return serverToken ? { [SERVER_TOKEN_HEADER]: serverToken } : {};
}

export function wsAuthProtocols(): string[] {
  return serverToken ? [WS_AUTH_PROTOCOL, serverToken] : [WS_AUTH_PROTOCOL];
}

function notifySessionExpired(): void {
  window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
}

function withAuth(init: RequestInit): RequestInit {
  return { ...init, headers: { ...(init.headers as Record<string, string> | undefined), ...authHeaders() } };
}

export async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const response = await fetch(input, withAuth(init));
  if (response.status !== 401) return response;
  // 会话/令牌失效:不再弹共享令牌输入框(账号体系已取代静态令牌),通知账号门重新登录。
  clearServerToken();
  notifySessionExpired();
  return response;
}
