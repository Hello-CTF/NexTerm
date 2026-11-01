import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GestureRecognizer, LONG_PRESS_MS, type GestureAction } from "../../features/remote/touchGestures";
import { KEY_COMBOS, filterCombos, keysymOf } from "../../features/remote/keys";

function run(mode: "direct" | "trackpad") {
  const actions: GestureAction[] = [];
  const recognizer = new GestureRecognizer(mode, (action) => actions.push(action), { x: 100, y: 100 });
  return { actions, recognizer };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("GestureRecognizer", () => {
  it("clicks where a direct tap lands", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 40, y: 50 }]);
    recognizer.touchEnd(0);
    expect(actions).toEqual([
      { type: "move", at: { x: 40, y: 50 } },
      { type: "button", button: 0, down: true, at: { x: 40, y: 50 } },
      { type: "button", button: 0, down: false, at: { x: 40, y: 50 } },
    ]);
  });

  it("drags with the left button held in direct mode", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 10, y: 10 }]);
    recognizer.touchMove([{ x: 60, y: 10 }]);
    recognizer.touchEnd(0);
    expect(actions.map((action) => (action.type === "button" ? `${action.button}${action.down ? "v" : "^"}` : action.type))).toEqual(["move", "0v", "move", "0^"]);
  });

  it("moves the virtual cursor by the finger distance and clicks at the cursor", () => {
    const { actions, recognizer } = run("trackpad");
    recognizer.touchStart([{ x: 10, y: 10 }]);
    recognizer.touchMove([{ x: 30, y: 25 }]);
    recognizer.touchEnd(0);
    expect(actions).toEqual([{ type: "move", at: { x: 120, y: 115 } }]);
    recognizer.touchStart([{ x: 200, y: 200 }]);
    recognizer.touchEnd(0);
    expect(actions.slice(1)).toEqual([
      { type: "move", at: { x: 120, y: 115 } },
      { type: "button", button: 0, down: true, at: { x: 120, y: 115 } },
      { type: "button", button: 0, down: false, at: { x: 120, y: 115 } },
    ]);
  });

  it("right-clicks on a long press and does not also click on release", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 5, y: 5 }]);
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    recognizer.touchEnd(0);
    const buttons = actions.filter((action) => action.type === "button");
    expect(buttons).toEqual([
      { type: "button", button: 2, down: true, at: { x: 5, y: 5 } },
      { type: "button", button: 2, down: false, at: { x: 5, y: 5 } },
    ]);
  });

  it("cancels the long press when the finger moves", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 5, y: 5 }]);
    recognizer.touchMove([{ x: 50, y: 5 }]);
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    expect(actions.some((action) => action.type === "button" && action.button === 2)).toBe(false);
  });

  it("scrolls with two fingers moving together", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 10, y: 100 }, { x: 60, y: 100 }]);
    recognizer.touchMove([{ x: 10, y: 80 }, { x: 60, y: 80 }]);
    expect(actions).toHaveLength(1);
    expect(actions[0]).toMatchObject({ type: "wheel", deltaY: 20 });
    expect(Math.abs((actions[0] as { deltaX: number }).deltaX)).toBe(0);
  });

  it("zooms and pans when two fingers spread", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 100, y: 100 }, { x: 150, y: 100 }]);
    recognizer.touchMove([{ x: 80, y: 100 }, { x: 170, y: 100 }]);
    expect(actions[0]).toMatchObject({ type: "pinch" });
    expect((actions[0] as { factor: number }).factor).toBeCloseTo(1.8);
    expect(actions.some((action) => action.type === "wheel")).toBe(false);
  });

  it("releases a held drag when a second finger arrives", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 10, y: 10 }]);
    recognizer.touchMove([{ x: 60, y: 10 }]);
    recognizer.touchStart([{ x: 60, y: 10 }, { x: 90, y: 10 }]);
    expect(actions[actions.length - 1]).toMatchObject({ type: "button", button: 0, down: false });
  });
});

describe("key tables", () => {
  it("maps codes to X11 keysyms", () => {
    expect(keysymOf("ControlLeft")).toBe(0xffe3);
    expect(keysymOf("Delete")).toBe(0xffff);
    expect(keysymOf("F5")).toBe(0xffc2);
    expect(keysymOf("KeyL")).toBe(0x6c);
    expect(keysymOf("Digit7")).toBe(0x37);
    expect(keysymOf("Nope")).toBeNull();
  });

  it("offers the combos the toolbar promises, grouped", () => {
    const labels = KEY_COMBOS.map((combo) => combo.label);
    for (const wanted of ["Ctrl+Alt+Del", "Win", "Alt+Tab", "Ctrl+Shift+Esc", "Win+L"]) expect(labels).toContain(wanted);
    expect(new Set(KEY_COMBOS.map((combo) => combo.group)).size).toBeGreaterThan(1);
  });

  it("filters combos by label and hint", () => {
    expect(filterCombos("alt+tab").map((combo) => combo.id)).toEqual(["alttab"]);
    expect(filterCombos("任务管理器").map((combo) => combo.id)).toEqual(["taskmgr"]);
    expect(filterCombos("zzz")).toEqual([]);
    expect(filterCombos("")).toHaveLength(KEY_COMBOS.length);
  });
});

describe("GestureRecognizer edge cases", () => {
  it("does not click or drag after a two-finger gesture while a finger is still down", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 10, y: 100 }, { x: 60, y: 100 }]);
    recognizer.touchEnd(1);
    recognizer.touchMove([{ x: 60, y: 200 }]);
    recognizer.touchEnd(0);
    expect(actions).toEqual([]);
    // A new one-finger tap works again once every finger lifted.
    recognizer.touchStart([{ x: 1, y: 2 }]);
    recognizer.touchEnd(0);
    expect(actions.filter((action) => action.type === "button")).toHaveLength(2);
  });

  it("ignores a third finger joining a one-finger touch that already became multi-touch", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 10, y: 100 }, { x: 60, y: 100 }]);
    recognizer.touchStart([{ x: 10, y: 100 }]);
    recognizer.touchEnd(0);
    expect(actions).toEqual([]);
  });

  it("keeps pinching once the threshold was crossed, even for small later changes", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 100, y: 100 }, { x: 150, y: 100 }]);
    recognizer.touchMove([{ x: 90, y: 100 }, { x: 160, y: 100 }]);
    recognizer.touchMove([{ x: 89, y: 100 }, { x: 161, y: 100 }]);
    expect(actions.filter((action) => action.type === "pinch")).toHaveLength(2);
    expect(actions.some((action) => action.type === "wheel")).toBe(false);
  });

  it("treats a wobble within the tap slop as a tap at the touch-down point", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 10, y: 10 }]);
    recognizer.touchMove([{ x: 15, y: 14 }]);
    recognizer.touchEnd(0);
    expect(actions.filter((action) => action.type === "button")).toEqual([
      { type: "button", button: 0, down: true, at: { x: 10, y: 10 } },
      { type: "button", button: 0, down: false, at: { x: 10, y: 10 } },
    ]);
  });

  it("scrolls at the virtual cursor in trackpad mode", () => {
    const { actions, recognizer } = run("trackpad");
    recognizer.touchStart([{ x: 10, y: 100 }, { x: 60, y: 100 }]);
    recognizer.touchMove([{ x: 10, y: 90 }, { x: 60, y: 90 }]);
    expect(actions[0]).toMatchObject({ type: "wheel", at: { x: 100, y: 100 } });
  });

  it("right-clicks at the cursor in trackpad mode and does not move the cursor from a still long press", () => {
    const { actions, recognizer } = run("trackpad");
    recognizer.touchStart([{ x: 300, y: 300 }]);
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    expect(actions[1]).toEqual({ type: "button", button: 2, down: true, at: { x: 100, y: 100 } });
    expect(recognizer.getCursor()).toEqual({ x: 100, y: 100 });
    recognizer.setCursor({ x: 5, y: 6 });
    expect(recognizer.getCursor()).toEqual({ x: 5, y: 6 });
  });

  it("ignores movement after a long press fired", () => {
    const { actions, recognizer } = run("direct");
    recognizer.touchStart([{ x: 5, y: 5 }]);
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    const count = actions.length;
    recognizer.touchMove([{ x: 90, y: 90 }]);
    expect(actions).toHaveLength(count);
  });

  it("cancel drops the long press and a held drag, and a trackpad drag has no button to release", () => {
    const direct = run("direct");
    direct.recognizer.touchStart([{ x: 5, y: 5 }]);
    direct.recognizer.cancel();
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    expect(direct.actions).toEqual([]);
    direct.recognizer.touchStart([{ x: 5, y: 5 }]);
    direct.recognizer.touchMove([{ x: 50, y: 5 }]);
    direct.recognizer.cancel();
    expect(direct.actions[direct.actions.length - 1]).toMatchObject({ type: "button", down: false });

    const pad = run("trackpad");
    pad.recognizer.touchStart([{ x: 5, y: 5 }]);
    pad.recognizer.touchMove([{ x: 50, y: 5 }]);
    const before = pad.actions.length;
    pad.recognizer.cancel();
    expect(pad.actions).toHaveLength(before);
  });

  it("switches mode between gestures", () => {
    const { actions, recognizer } = run("direct");
    recognizer.setMode("trackpad");
    recognizer.touchStart([{ x: 300, y: 300 }]);
    recognizer.touchEnd(0);
    expect(actions[0]).toEqual({ type: "move", at: { x: 100, y: 100 } });
  });
});
