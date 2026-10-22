// Keep in sync with SESSION_EXPIRED_EVENT in src/ipc/authApi.ts: notify the auth gate to re-login on 401.
import { currentAuthSessionEpoch } from "./authSessionEpoch";

const SESSION_EXPIRED_EVENT = "nexterm:session-expired";

function notifySessionExpired(): void {
  window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
}

export async function authedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const startedEpoch = currentAuthSessionEpoch();
  const response = await fetch(input, init);
  if (response.status !== 401) return response;
  if (currentAuthSessionEpoch() === startedEpoch) notifySessionExpired();
  return response;
}
