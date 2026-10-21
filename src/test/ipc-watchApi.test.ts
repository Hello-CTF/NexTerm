import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn(), listenEvent: vi.fn() }));
vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call });
});
vi.mock("../ipc/events", async () => {
  const actual = await vi.importActual<typeof import("../ipc/events")>("../ipc/events");
  return { ...actual, listenEvent: mocks.listenEvent };
});

import { watchApi, watchPanelApi, type WatchToolDataEventPayload } from "../ipc/watchApi";
import type { WatchApi } from "../features/ai/watchTypes";

// Compile-time conformance: the production adapter implements the reviewed
// panel contract through typed IPC calls only (no unsafeCall, no mock as
// production).
const apiConformance: WatchApi = watchPanelApi;

function toolDataEvent(ownerId: string, deliveryId: string): WatchToolDataEventPayload {
  return {
    ownerId,
    data: {
      channel: "tool",
      untrusted: true,
      event: {
        kind: "watchEvent",
        deliveryId,
        runId: "run-1",
        watchId: "watch-1",
        targetId: "target-1",
        condition: { type: "contains", pattern: "ready" },
        matchedFrom: 0,
        matchedTo: 5,
        excerpt: "ready",
        firedAt: 1000,
      },
    },
  };
}

describe("watch IPC adapter", () => {
  beforeEach(() => vi.clearAllMocks());

  it("implements the reviewed WatchApi contract", () => {
    expect(typeof apiConformance.listWatches).toBe("function");
  });

  it("forwards typed ai_watch_* commands without unsafeCall", async () => {
    mocks.call.mockResolvedValue([]);
    await watchApi.listWatches();
    expect(mocks.call).toHaveBeenNthCalledWith(1, "ai_watch_list");
    await watchApi.listTargets();
    expect(mocks.call).toHaveBeenNthCalledWith(2, "ai_watch_targets");

    mocks.call.mockResolvedValue({ id: "watch-1", status: "cancelled" });
    await watchApi.cancelWatch("watch-1", 7);
    expect(mocks.call).toHaveBeenNthCalledWith(3, "ai_watch_cancel", { watchId: "watch-1", expectedRevision: 7 });

    await watchApi.createWatch({
      targetId: "target-1",
      name: "ready",
      condition: { type: "contains", pattern: "ready" },
      repeat: false,
      expiresAt: 0,
    });
    expect(mocks.call).toHaveBeenNthCalledWith(4, "ai_watch_create", {
      targetId: "target-1",
      name: "ready",
      condition: { type: "contains", pattern: "ready" },
      repeat: false,
      expiresAt: 0,
    });
  });

  it("filters the tool-data subscription by owner and validates the envelope", async () => {
    let handler: ((payload: unknown) => void) | null = null;
    const unlisten = vi.fn();
    mocks.listenEvent.mockImplementation((_event: string, h: (payload: unknown) => void) => {
      handler = h;
      return Promise.resolve(unlisten);
    });
    const received: string[] = [];
    const off = watchApi.subscribeWatchToolData("owner-a", (data) => received.push(data.event.deliveryId));
    expect(handler).not.toBeNull();
    await Promise.resolve();

    handler!(toolDataEvent("owner-a", "delivery-1"));
    handler!(toolDataEvent("owner-b", "delivery-2"));
    handler!(toolDataEvent("", "delivery-3"));
    handler!({ ownerId: "owner-a", data: { channel: "user", untrusted: false } });
    handler!(null);
    expect(received).toEqual(["delivery-1", "delivery-3"]);

    off();
    expect(unlisten).toHaveBeenCalledTimes(1);
  });

  it("drops events for other owners per the server-side filter contract", () => {
    // The server already filters by subscriber identity; the adapter keeps a
    // defensive owner check so a misrouted event never crosses owners.
    let handler: ((payload: unknown) => void) | null = null;
    mocks.listenEvent.mockImplementation((_event: string, h: (payload: unknown) => void) => {
      handler = h;
      return Promise.resolve(vi.fn());
    });
    const received: string[] = [];
    watchApi.subscribeWatchToolData("owner-b", (data) => received.push(data.event.deliveryId));
    handler!(toolDataEvent("owner-a", "delivery-x"));
    expect(received).toEqual([]);
  });
});
