// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { replayPendingWebSyncRequest, trackWebSyncRequests, WEB_SYNC_REQUEST_EVENT } from "../../app/syncEvents";

let stop: (() => void) | null = null;

afterEach(() => {
  stop?.();
  stop = null;
});

describe("web sync request tracking", () => {
  it("replays a request fired before the auto-sync listener existed", () => {
    stop = trackWebSyncRequests();
    window.dispatchEvent(new Event(WEB_SYNC_REQUEST_EVENT));
    const late = vi.fn();
    window.addEventListener(WEB_SYNC_REQUEST_EVENT, late);
    replayPendingWebSyncRequest();
    window.removeEventListener(WEB_SYNC_REQUEST_EVENT, late);
    expect(late).toHaveBeenCalledTimes(1);
  });

  it("does not replay when nothing was requested", () => {
    stop = trackWebSyncRequests();
    const late = vi.fn();
    window.addEventListener(WEB_SYNC_REQUEST_EVENT, late);
    replayPendingWebSyncRequest();
    window.removeEventListener(WEB_SYNC_REQUEST_EVENT, late);
    expect(late).not.toHaveBeenCalled();
  });
});
