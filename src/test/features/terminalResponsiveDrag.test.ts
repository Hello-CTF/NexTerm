import { describe, expect, it, vi } from "vitest";
import {
  createFrameCoalescer,
  widthForKey,
  widthForPointer,
} from "../../ui/ResizeHandle";

describe("coalesced resize interactions", () => {
  it("publishes only the latest intermediate frame and the exact pointer-up value", () => {
    const callbacks: Array<() => void> = [];
    const published: number[] = [];
    const frames = createFrameCoalescer<number>((value) => published.push(value), {
      request: (callback) => {
        callbacks.push(callback);
        return callbacks.length;
      },
      cancel: vi.fn(),
    });

    frames.schedule(250);
    frames.schedule(280);
    frames.schedule(310);
    expect(published).toEqual([]);
    callbacks[0]();
    expect(published).toEqual([310]);

    frames.schedule(330);
    frames.flush(345);
    expect(published).toEqual([310, 345]);
  });

  it("flushes a pending value on cancel and drops work on unmount", () => {
    const callbacks: Array<() => void> = [];
    const publish = vi.fn();
    const frames = createFrameCoalescer<number>(publish, {
      request: (callback) => {
        callbacks.push(callback);
        return callbacks.length;
      },
      cancel: () => undefined,
    });
    frames.schedule(270);
    frames.flush();
    expect(publish).toHaveBeenLastCalledWith(270);

    frames.schedule(290);
    frames.cancel();
    callbacks[1]();
    expect(publish).toHaveBeenCalledTimes(1);
  });

  it("supports both sidebar directions and keyboard bounds", () => {
    expect(widthForPointer("left", 248, 100, 320, 180, 560)).toBe(468);
    expect(widthForPointer("right", 352, 400, 180, 300, 760)).toBe(572);
    expect(widthForPointer("left", 248, 100, -1000, 180, 560)).toBe(180);
    expect(widthForKey("left", 248, "ArrowRight", 180, 560)).toBe(258);
    expect(widthForKey("right", 352, "ArrowRight", 300, 760, true)).toBe(302);
    expect(widthForKey("left", 248, "Home", 180, 560)).toBe(180);
    expect(widthForKey("right", 352, "End", 300, 760)).toBe(760);
  });
});
