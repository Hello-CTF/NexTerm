/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";

vi.mock("../../ipc/env", () => ({ WEB: false }));

import { resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import { attachTerminalInput } from "../../features/terminal/terminalInput/attachTerminalInput";
import {
  KITTY_KEYBOARD_SWITCH_KEY,
  kittyKeyboardActive,
  kittyKeyboardSwitch,
} from "../../features/terminal/terminalInput/kittyKeyboard";

let cleanup: (() => void) | null = null;

function setup(kittyKeyboard: boolean) {
  const host = document.createElement("div");
  document.body.append(host);
  const term = new Terminal({ vtExtensions: { kittyKeyboard } });
  term.open(host);
  const detach = attachTerminalInput(term, host, { kittyKeyboard });
  const sent: string[] = [];
  term.onData((data) => sent.push(data));
  const press = (init: KeyboardEventInit) => {
    term.textarea!.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));
    term.textarea!.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, cancelable: true, ...init }));
  };
  const write = (data: string) => new Promise<void>((resolve) => term.write(data, resolve));
  cleanup = () => {
    detach();
    term.dispose();
    host.remove();
  };
  return { term, sent, press, write };
}

const SHIFT_ENTER = { key: "Enter", code: "Enter", keyCode: 13, shiftKey: true };
const CTRL_SHIFT_W = { key: "W", code: "KeyW", keyCode: 87, ctrlKey: true, shiftKey: true };
// CSI > 1 u: the program pushes the "disambiguate escape codes" flag.
const PUSH_DISAMBIGUATE = "\x1b[>1u";

beforeEach(() => {
  localStorage.clear();
  resetAllKeybindings();
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
});

afterEach(() => {
  cleanup?.();
  cleanup = null;
  resetAllKeybindings();
  vi.unstubAllGlobals();
});

describe("kitty keyboard protocol switch", () => {
  it("is off unless the device-local switch is set", () => {
    expect(kittyKeyboardSwitch()).toBe(false);
    localStorage.setItem(KITTY_KEYBOARD_SWITCH_KEY, "on");
    expect(kittyKeyboardSwitch()).toBe(true);
  });

  it("keeps legacy bytes when the switch is off, even if a program asks for the protocol", async () => {
    const view = setup(false);
    await view.write(PUSH_DISAMBIGUATE);
    expect(kittyKeyboardActive(view.term, false)).toBe(false);
    view.press(SHIFT_ENTER);
    expect(view.sent).toEqual(["\x1b\r"]);
  });

  it("hands encoding to xterm once a program enables the protocol, while app shortcuts stay withheld", async () => {
    const view = setup(true);
    view.press(SHIFT_ENTER);
    expect(view.sent).toEqual(["\x1b\r"]);

    await view.write(PUSH_DISAMBIGUATE);
    expect(kittyKeyboardActive(view.term, true)).toBe(true);
    view.press(SHIFT_ENTER);
    view.press(CTRL_SHIFT_W);
    expect(view.sent).toEqual(["\x1b\r", "\x1b[13;2u"]);

    setKeybinding("closePane", null);
    view.press(CTRL_SHIFT_W);
    expect(view.sent).toEqual(["\x1b\r", "\x1b[13;2u", "\x1b[119;6u"]);
  });
});
