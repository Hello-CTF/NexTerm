import { describe, expect, it } from "vitest";
import {
  THROTTLE_ACTIVE_MS,
  THROTTLE_RECOVERED_MS,
  throttleStateFrom,
  throttleView,
} from "../../features/terminal/terminalThrottle";

describe("terminal throttle tracker", () => {
  it("starts with no view before any event", () => {
    expect(throttleView(null, 1000)).toBeNull();
  });

  it("shows active with inflight bytes inside the quiet window", () => {
    const state = throttleStateFrom({ tabId: "t1", inflightBytes: 4096 }, 1000);
    expect(throttleView(state, 1000)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 4096,
    });
    expect(throttleView(state, 1000 + THROTTLE_ACTIVE_MS)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 4096,
    });
  });

  it("shows recovered after the quiet window and expires after the display window", () => {
    const state = throttleStateFrom({ tabId: "t1", inflightBytes: 4096 }, 1000);
    expect(throttleView(state, 1000 + THROTTLE_ACTIVE_MS + 1)).toEqual({
      active: false,
      recovered: true,
      inflightBytes: 0,
    });
    expect(
      throttleView(state, 1000 + THROTTLE_ACTIVE_MS + THROTTLE_RECOVERED_MS),
    ).toEqual({ active: false, recovered: true, inflightBytes: 0 });
    expect(
      throttleView(state, 1000 + THROTTLE_ACTIVE_MS + THROTTLE_RECOVERED_MS + 1),
    ).toBeNull();
  });

  it("re-arms the active window when a new episode arrives", () => {
    const first = throttleStateFrom({ tabId: "t1", inflightBytes: 1024 }, 1000);
    const second = throttleStateFrom({ tabId: "t1", inflightBytes: 2048 }, 1000 + THROTTLE_ACTIVE_MS);
    expect(throttleView(second, 1000 + THROTTLE_ACTIVE_MS)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 2048,
    });
    expect(first.lastEventAt).toBeLessThan(second.lastEventAt);
  });
});
