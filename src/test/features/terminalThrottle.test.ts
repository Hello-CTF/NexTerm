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
    const state = throttleStateFrom({ tabId: "t1", inflightBytes: 4096, version: 1 }, 1000);
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

  it("shows recovered only after a real drain event, then expires", () => {
    const recovered = throttleStateFrom(
      { tabId: "t1", inflightBytes: 128, recovered: true, version: 2 },
      2000,
    );
    expect(throttleView(recovered, 2000)).toEqual({
      active: false,
      recovered: true,
      inflightBytes: 0,
    });
    expect(throttleView(recovered, 2000 + THROTTLE_RECOVERED_MS)).toEqual({
      active: false,
      recovered: true,
      inflightBytes: 0,
    });
    expect(throttleView(recovered, 2000 + THROTTLE_RECOVERED_MS + 1)).toBeNull();
  });

  it("re-arms active when a new episode arrives after recovery", () => {
    const recovered = throttleStateFrom(
      { tabId: "t1", inflightBytes: 128, recovered: true, version: 2 },
      2000,
    );
    const reentry = throttleStateFrom({ tabId: "t1", inflightBytes: 8192, version: 3 }, 2500);
    expect(throttleView(recovered, 2500)?.recovered).toBe(true);
    expect(throttleView(reentry, 2500)).toEqual({
      active: true,
      recovered: false,
      inflightBytes: 8192,
    });
  });
});
