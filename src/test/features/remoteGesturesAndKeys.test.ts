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
