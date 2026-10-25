// Sync event names and the request tracker live in a dependency-free module so App and the
// lazily loaded sync code can both use them without waiting on the SyncCard chunk.
export const WEB_SYNC_REQUEST_EVENT = "nexterm:web-sync-request";
export const WEB_SYNC_RESULT_EVENT = "nexterm:web-sync-result";
export const WEB_SYNC_ERROR_EVENT = "nexterm:web-sync-error";

let pendingRequest = false;
let tracking = false;

// Remembers a sync request fired before the lazily mounted auto-sync listener exists.
export function trackWebSyncRequests(): () => void {
  if (typeof window === "undefined") return () => undefined;
  const mark = () => {
    pendingRequest = true;
  };
  window.addEventListener(WEB_SYNC_REQUEST_EVENT, mark);
  tracking = true;
  return () => {
    window.removeEventListener(WEB_SYNC_REQUEST_EVENT, mark);
    tracking = false;
    pendingRequest = false;
  };
}

// Called once the real listener is attached; replays a request that arrived earlier.
export function replayPendingWebSyncRequest(): void {
  if (!tracking || !pendingRequest) return;
  pendingRequest = false;
  window.dispatchEvent(new Event(WEB_SYNC_REQUEST_EVENT));
}
