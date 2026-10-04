import { describe, expect, it } from "vitest";
import {
  THROTTLE_RECOVERED_MS,
  throttleStateFrom,
  throttleView,
} from "../../features/terminal/terminalThrottle";

describe("terminal throttle tracker", () => {
  it("starts with no view before any event", () => {
    expect(throttleView(null, 1000)).toBeNull();
  });

  it("shows active on entry and stays active without a recovery event", () => {
    const state = throttleStateFrom(null, { tabId: "t1", channelId: "a-1", inflightBytes: 4096, version: 1 }, 1000);
    expect(throttleView(state, 1000)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 4096,
    });
    expect(throttleView(state, 1000 + 60_000)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 4096,
    });
  });

  it("clears the whole channel set on the authoritative tab-level recovered", () => {
    const entryA = throttleStateFrom(null, { tabId: "t1", channelId: "a-1", inflightBytes: 4096, version: 1 }, 1000);
    const entryB = throttleStateFrom(entryA, { tabId: "t1", channelId: "b-1", inflightBytes: 8192, version: 2 }, 1100);
    expect(throttleView(entryB, 1100)?.inflightBytes).toBe(8192);

    const recovered = throttleStateFrom(entryB, { tabId: "t1", channelId: "b-1", inflightBytes: 64, recovered: true, version: 3 }, 1200);
    expect(recovered.channels).toEqual({});
    expect(throttleView(recovered, 1200)).toEqual({
      active: false,
      recovered: true,
      inflightBytes: 0,
    });
    expect(throttleView(recovered, 1200 + THROTTLE_RECOVERED_MS + 1)).toBeNull();
  });

  it("ignores duplicate recovered events when already clear", () => {
    const entryA = throttleStateFrom(null, { tabId: "t1", channelId: "a-1", inflightBytes: 4096, version: 1 }, 1000);
    const drainedA = throttleStateFrom(entryA, { tabId: "t1", channelId: "a-1", inflightBytes: 128, recovered: true, version: 2 }, 1100);
    expect(throttleView(drainedA, 1100)?.recovered).toBe(true);

    const duplicate = throttleStateFrom(drainedA, { tabId: "t1", channelId: "b-1", inflightBytes: 0, recovered: true, version: 3 }, 1200);
    expect(duplicate).toBe(drainedA);
    expect(throttleView(null, 1300)).toBeNull();
  });

  it("re-arms active when a new episode arrives after recovery", () => {
    const entry = throttleStateFrom(null, { tabId: "t1", channelId: "a-1", inflightBytes: 4096, version: 1 }, 1000);
    const drained = throttleStateFrom(entry, { tabId: "t1", channelId: "a-1", inflightBytes: 128, recovered: true, version: 2 }, 1100);
    expect(throttleView(drained, 1100)?.recovered).toBe(true);
    const reentry = throttleStateFrom(drained, { tabId: "t1", channelId: "a-2", inflightBytes: 8192, version: 3 }, 1500);
    expect(throttleView(reentry, 1500)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 8192,
    });
  });
});
