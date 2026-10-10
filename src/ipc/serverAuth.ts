// 与 src/ipc/authApi.ts 的 SESSION_EXPIRED_EVENT 保持一致:401 时通知账号门重新登录。
const SESSION_EXPIRED_EVENT = "nexterm:session-expired";

function notifySessionExpired(): void {
  window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
}

export async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const response = await fetch(input, init);
  if (response.status !== 401) return response;
  notifySessionExpired();
  return response;
}
