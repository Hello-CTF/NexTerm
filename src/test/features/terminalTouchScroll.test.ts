/** @vitest-environment jsdom */

// The touch scroll recognizer against a stand-in terminal: which scroll path a swipe takes in each terminal mode,
// how it tells swipes from taps, and how a fling runs and stops (driven by an injected frame clock).

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { attachTouchScroll, type TouchScrollHandle, type TouchScrollOptions } from "../../features/terminal/touchScroll";

const CELL = 20;

interface FakeTerminal {
  element: HTMLElement;
  modes: { mouseTrackingMode: string };
  buffer: { active: { type: string } };
  scrollLines: ReturnType<typeof vi.fn<(amount: number) => void>>;
}

let host: HTMLElement;
let screen: HTMLElement;
let term: FakeTerminal;
let handle: TouchScrollHandle | null;
let wheels: WheelEvent[];
let clock: number;
let frames: Array<() => void>;

function attach(options: TouchScrollOptions = {}): TouchScrollHandle {
  handle = attachTouchScroll(term, host, {
    cellHeight: () => CELL,
    reducedMotion: () => false,
    now: () => clock,
    requestFrame: (callback) => {
      frames.push(() => callback(clock));
      return frames.length;
    },
    cancelFrame: (id) => {
      frames[id - 1] = () => {};
    },
    ...options,
  });
  return handle;
}

function runFrame(ms = 16): void {
  clock += ms;
  const pending = frames;
  frames = [];
  for (const frame of pending) frame();
}

type Point = { x: number; y: number; id?: number };

function touch(type: "touchstart" | "touchmove" | "touchend" | "touchcancel", points: Point[], target: Element = screen) {
  const event = new TouchEvent(type, { bubbles: true, cancelable: true });
  const list = (items: Point[]) => {
    const touches = items.map((p) => ({ identifier: p.id ?? 0, clientX: p.x, clientY: p.y, pageX: p.x, pageY: p.y, target }));
    return Object.assign([...touches], { item: (index: number) => touches[index] ?? null });
  };
  const ended = type === "touchend" || type === "touchcancel";
  Object.defineProperty(event, "touches", { value: list(ended ? [] : points) });
  Object.defineProperty(event, "targetTouches", { value: list(ended ? [] : points) });
  Object.defineProperty(event, "changedTouches", { value: list(points) });
  target.dispatchEvent(event);
  return event;
}

// A swipe from y0 to y1 at x: crosses the slop first, then moves in one step per entry of `steps`.
function swipe(x: number, y0: number, steps: number[], { end = true, msPerStep = 16 } = {}) {
  touch("touchstart", [{ x, y: y0 }]);
  const moves: TouchEvent[] = [];
  for (const y of steps) {
    clock += msPerStep;
    moves.push(touch("touchmove", [{ x, y }]));
  }
  if (end) touch("touchend", [{ x, y: steps[steps.length - 1] ?? y0 }]);
  return moves;
}

beforeEach(() => {
  host = document.createElement("div");
  const element = document.createElement("div");
  screen = document.createElement("div");
  screen.className = "xterm-screen";
  element.append(screen);
  host.append(element);
  document.body.append(host);
  term = { element, modes: { mouseTrackingMode: "none" }, buffer: { active: { type: "normal" } }, scrollLines: vi.fn<(amount: number) => void>() };
  wheels = [];
  screen.addEventListener("wheel", (event) => wheels.push(event));
  clock = 1000;
  frames = [];
  handle = null;
});

afterEach(() => {
  handle?.dispose();
  host.remove();
});

const scrolled = () => term.scrollLines.mock.calls.reduce((sum, [rows]) => sum + rows, 0);

describe("normal buffer", () => {
  it("scrolls the scrollback by whole rows as the finger moves, carrying the remainder", () => {
    attach();
    // Finger down past the slop (no scroll yet), then 30px down: one row back in history with 10px carried over.
    swipe(50, 100, [115, 145], { end: false, msPerStep: 200 });
    expect(term.scrollLines.mock.calls).toEqual([[-1]]);
    // 10 more pixels complete the second row.
    clock += 200;
    touch("touchmove", [{ x: 50, y: 155 }]);
    expect(term.scrollLines.mock.calls).toEqual([[-1], [-1]]);
  });

  it("moves the content with the finger: a finger moving up scrolls towards the newest output", () => {
    attach();
    swipe(50, 200, [185, 125], { msPerStep: 200 });
    expect(scrolled()).toBe(3);
  });

  it("cancels the touch move only once the drag is a scroll", () => {
    attach();
    touch("touchstart", [{ x: 50, y: 100 }]);
    expect(touch("touchmove", [{ x: 50, y: 106 }]).defaultPrevented).toBe(false);
    expect(touch("touchmove", [{ x: 50, y: 130 }]).defaultPrevented).toBe(true);
  });

  it("leaves jitter within the slop alone", () => {
    attach();
    swipe(50, 100, [104, 96, 109, 100]);
    expect(term.scrollLines).not.toHaveBeenCalled();
    expect(frames).toEqual([]);
  });

  it("leaves horizontal drags alone", () => {
    attach();
    touch("touchstart", [{ x: 50, y: 100 }]);
    const moves = [touch("touchmove", [{ x: 80, y: 105 }]), touch("touchmove", [{ x: 160, y: 110 }])];
    // Once the drag is classified as horizontal, later vertical movement does not take it over.
    moves.push(touch("touchmove", [{ x: 160, y: 300 }]));
    touch("touchend", [{ x: 160, y: 300 }]);
    expect(moves.some((event) => event.defaultPrevented)).toBe(false);
    expect(term.scrollLines).not.toHaveBeenCalled();
    expect(frames).toEqual([]);
  });

  it("ignores swipes that start outside the terminal screen", () => {
    attach();
    const scrollbar = document.createElement("div");
    host.append(scrollbar);
    touch("touchstart", [{ x: 50, y: 100 }], scrollbar);
    touch("touchmove", [{ x: 50, y: 200 }], scrollbar);
    expect(term.scrollLines).not.toHaveBeenCalled();
  });

  it("ignores two-finger gestures", () => {
    attach();
    touch("touchstart", [{ x: 50, y: 100 }]);
    touch("touchstart", [{ x: 50, y: 100 }, { x: 150, y: 100, id: 1 }]);
    touch("touchmove", [{ x: 50, y: 200 }, { x: 150, y: 200, id: 1 }]);
    expect(term.scrollLines).not.toHaveBeenCalled();
  });

  it("waits while the row height is unknown", () => {
    attach({ cellHeight: () => 0 });
    swipe(50, 100, [115, 300]);
    expect(term.scrollLines).not.toHaveBeenCalled();
  });

  it("keeps xterm's own touch scrolling from scrolling as well", () => {
    attach();
    const reached = vi.fn();
    screen.addEventListener("-xterm-gesturechange", reached);
    screen.dispatchEvent(new Event("-xterm-gesturechange", { bubbles: false, cancelable: true }));
    expect(reached).not.toHaveBeenCalled();
  });

  it("stops listening when disposed", () => {
    attach().dispose();
    handle = null;
    swipe(50, 100, [115, 300]);
    expect(term.scrollLines).not.toHaveBeenCalled();
  });
});

describe("fling", () => {
  it("keeps scrolling after a fast release and decelerates to a stop", () => {
    attach();
    // 60px in 32ms: about 1.9px/ms upwards.
    swipe(50, 300, [289, 260, 240], { msPerStep: 16 });
    const atRelease = scrolled();
    const firstSecond: number[] = [];
    let frameCount = 0;
    while (frames.length > 0 && frameCount < 2000) {
      runFrame();
      frameCount++;
      if (frameCount === 30 || frameCount === 60) firstSecond.push(scrolled());
    }
    expect(frames).toEqual([]);
    expect(frameCount).toBeLessThan(2000);
    const [half, full] = firstSecond;
    // Decelerating: the second half second covers less ground than the first, all of it towards newer output.
    expect(half - atRelease).toBeGreaterThan(full - half);
    expect(full - half).toBeGreaterThan(0);
    expect(term.scrollLines.mock.calls.every(([rows]) => rows > 0)).toBe(true);
  });

  it("stops on the next touch, which is then not a tap", () => {
    const touchScroll = attach();
    swipe(50, 300, [289, 260, 240], { msPerStep: 16 });
    runFrame();
    const atTouch = scrolled();
    touch("touchstart", [{ x: 50, y: 200 }]);
    expect(touchScroll.stoppedFling()).toBe(true);
    for (let i = 0; i < 20; i++) runFrame();
    expect(scrolled()).toBe(atTouch);
    touch("touchend", [{ x: 50, y: 200 }]);
    // A later touch with nothing moving is an ordinary tap again.
    touch("touchstart", [{ x: 50, y: 200 }]);
    expect(touchScroll.stoppedFling()).toBe(false);
  });

  it("does not fling a slow release", () => {
    attach();
    swipe(50, 300, [289, 280, 270], { msPerStep: 200 });
    expect(frames).toEqual([]);
  });

  it("does not fling under prefers-reduced-motion", () => {
    attach({ reducedMotion: () => true });
    swipe(50, 300, [289, 260, 240], { msPerStep: 16 });
    expect(frames).toEqual([]);
  });

  it("does not fling a cancelled touch", () => {
    attach();
    swipe(50, 300, [289, 260, 240], { end: false, msPerStep: 16 });
    touch("touchcancel", [{ x: 50, y: 240 }]);
    expect(frames).toEqual([]);
  });
});

describe("applications", () => {
  it("sends one wheel step per row at the finger when the application tracks the mouse", () => {
    term.modes.mouseTrackingMode = "vt200";
    attach();
    swipe(70, 100, [115, 175], { msPerStep: 200 });
    expect(term.scrollLines).not.toHaveBeenCalled();
    expect(wheels.map((event) => [event.deltaY, event.deltaMode, event.clientX, event.clientY])).toEqual([
      [-1, WheelEvent.DOM_DELTA_LINE, 70, 175],
      [-1, WheelEvent.DOM_DELTA_LINE, 70, 175],
      [-1, WheelEvent.DOM_DELTA_LINE, 70, 175],
    ]);
  });

  it("sends wheel steps in the alternate screen so xterm turns them into cursor keys", () => {
    term.buffer.active.type = "alternate";
    attach();
    swipe(70, 200, [185, 145], { msPerStep: 200 });
    expect(term.scrollLines).not.toHaveBeenCalled();
    expect(wheels.map((event) => event.deltaY)).toEqual([1, 1]);
  });
});
