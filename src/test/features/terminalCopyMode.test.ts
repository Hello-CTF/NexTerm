/** @vitest-environment jsdom */

// Keyboard copy mode driven with key events on a real xterm: motions over wrapped lines and wide characters,
// selections copied to the clipboard, and terminal input withheld while it is active.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";

vi.mock("../../ipc/env", () => ({ WEB: false }));

import { attachTerminalInput } from "../../features/terminal/terminalInput/attachTerminalInput";
import { createCopyMode, type CopyModeController } from "../../features/terminal/copyMode";

const cleanups: (() => void)[] = [];

interface Setup {
  term: Terminal;
  copy: CopyModeController;
  copied: string[];
  sent: string[];
  key: (key: string, mods?: { ctrl?: boolean; shift?: boolean }) => void;
  write: (data: string) => Promise<void>;
}

function setup(options: { cols?: number; rows?: number; available?: boolean } = {}): Setup {
  const host = document.createElement("div");
  document.body.append(host);
  const term = new Terminal({ cols: options.cols ?? 40, rows: options.rows ?? 6, scrollback: 100, allowProposedApi: true });
  term.open(host);
  const copied: string[] = [];
  const copy = createCopyMode(term, host, {
    copy: async (text) => {
      copied.push(text);
    },
    available: () => options.available ?? true,
  });
  const detach = attachTerminalInput(term, host, { kittyKeyboard: false, webkitCommitEnter: false });
  const sent: string[] = [];
  term.onData((data) => {
    if (data) sent.push(data);
  });
  const textarea = term.textarea!;
  const key = (name: string, mods: { ctrl?: boolean; shift?: boolean } = {}) => {
    const code = /^[a-z]$/i.test(name) ? `Key${name.toUpperCase()}` : name;
    const keyCode = name === "Enter" ? 13 : name.length === 1 ? name.toUpperCase().charCodeAt(0) : 0;
    const init = { key: name, code, keyCode, ctrlKey: mods.ctrl ?? false, shiftKey: mods.shift ?? false, bubbles: true, cancelable: true };
    const down = new KeyboardEvent("keydown", init);
    textarea.dispatchEvent(down);
    if (!down.defaultPrevented && name.length === 1 && !mods.ctrl) {
      textarea.dispatchEvent(new KeyboardEvent("keypress", { ...init, charCode: name.charCodeAt(0) }));
    }
    textarea.dispatchEvent(new KeyboardEvent("keyup", init));
  };
  cleanups.push(() => {
    copy.dispose();
    detach();
    term.dispose();
    host.remove();
  });
  return {
    term,
    copy,
    copied,
    sent,
    key,
    write: (data) => new Promise<void>((resolve) => term.write(data, resolve)),
  };
}

const enterChord = (s: Setup) => s.key("X", { ctrl: true, shift: true });
const keys = (s: Setup, sequence: string) => {
  for (const ch of sequence) s.key(ch, { shift: ch !== ch.toLowerCase() || "$^?".includes(ch) });
};

beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
});

afterEach(() => {
  for (const fn of cleanups.splice(0)) fn();
  vi.unstubAllGlobals();
});

describe("terminal copy mode", () => {
  it("starts at the terminal cursor and moves by words and line edges", async () => {
    const s = setup();
    await s.write("first line\r\nalpha beta-gamma  delta\r\n$ ");
    enterChord(s);
    expect(s.copy.isActive()).toBe(true);
    expect(s.copy.state().cursor).toEqual({ x: 2, y: 2 });

    keys(s, "k0");
    expect(s.copy.state().cursor).toEqual({ x: 0, y: 1 });
    keys(s, "w");
    expect(s.copy.state().cursor).toEqual({ x: 6, y: 1 });
    keys(s, "w");
    expect(s.copy.state().cursor).toEqual({ x: 10, y: 1 });
    keys(s, "e");
    expect(s.copy.state().cursor).toEqual({ x: 15, y: 1 });
    keys(s, "$");
    expect(s.copy.state().cursor).toEqual({ x: 22, y: 1 });
    keys(s, "b");
    expect(s.copy.state().cursor).toEqual({ x: 18, y: 1 });
    // w continues onto the next line.
    keys(s, "w");
    expect(s.copy.state().cursor).toEqual({ x: 0, y: 2 });
    keys(s, "g");
    expect(s.copy.state().cursor).toEqual({ x: 0, y: 0 });
    s.key("ArrowRight");
    s.key("ArrowDown");
    expect(s.copy.state().cursor).toEqual({ x: 1, y: 1 });
  });

  it("treats a wrapped line as one line for word and line motions", async () => {
    const s = setup({ cols: 10 });
    await s.write("  one twoooooo three\r\n");
    enterChord(s);
    // Row 0 "  one twoo", row 1 (wrapped) "oooo three".
    s.key("k");
    s.key("k");
    expect(s.copy.state().cursor.y).toBe(0);
    keys(s, "^");
    expect(s.copy.state().cursor).toEqual({ x: 2, y: 0 });
    keys(s, "ww");
    expect(s.copy.state().cursor).toEqual({ x: 5, y: 1 });
    keys(s, "b");
    expect(s.copy.state().cursor).toEqual({ x: 6, y: 0 });
    keys(s, "$");
    expect(s.copy.state().cursor).toEqual({ x: 9, y: 1 });
    keys(s, "0");
    expect(s.copy.state().cursor).toEqual({ x: 0, y: 0 });
    keys(s, "v$y");
    expect(s.copied).toEqual(["  one twoooooo three"]);
  });

  it("steps over wide characters and keeps CJK words apart from ASCII ones", async () => {
    const s = setup();
    await s.write("ab中文cd 下一个\r\n");
    enterChord(s);
    s.key("k");
    keys(s, "0l");
    expect(s.copy.state().cursor).toEqual({ x: 1, y: 0 });
    keys(s, "l");
    expect(s.copy.state().cursor).toEqual({ x: 2, y: 0 });
    keys(s, "l");
    expect(s.copy.state().cursor).toEqual({ x: 4, y: 0 });
    // Left of a wide character's trailing half lands on its first column.
    keys(s, "h");
    expect(s.copy.state().cursor).toEqual({ x: 2, y: 0 });
    keys(s, "0w");
    expect(s.copy.state().cursor).toEqual({ x: 2, y: 0 });
    keys(s, "w");
    expect(s.copy.state().cursor).toEqual({ x: 6, y: 0 });
    keys(s, "w");
    expect(s.copy.state().cursor).toEqual({ x: 9, y: 0 });
    keys(s, "ve");
    keys(s, "y");
    expect(s.copied).toEqual(["下一个"]);
  });

  it("copies character, line and block selections and exits", async () => {
    const s = setup();
    await s.write("abc 123\r\ndef 456\r\nghi 789\r\n");
    enterChord(s);
    keys(s, "g");
    keys(s, "vey");
    expect(s.copied).toEqual(["abc"]);
    expect(s.copy.isActive()).toBe(false);

    enterChord(s);
    keys(s, "gjVjy");
    expect(s.copied[1]).toBe("def 456\nghi 789");

    enterChord(s);
    keys(s, "gw");
    s.key("v", { ctrl: true });
    keys(s, "jjl");
    expect(s.copy.blockRects()).toEqual([
      { y: 0, x0: 4, x1: 6 },
      { y: 1, x0: 4, x1: 6 },
      { y: 2, x0: 4, x1: 6 },
    ]);
    s.key("Enter");
    expect(s.copied[2]).toBe("12\n45\n78");
  });

  it("asks for a selection before y copies anything", async () => {
    const s = setup();
    await s.write("abc\r\n");
    enterChord(s);
    keys(s, "y");
    expect(s.copied).toEqual([]);
    expect(s.copy.isActive()).toBe(true);
    expect(s.copy.state().message).toBeTruthy();
  });

  it("searches forward and backward and repeats with n and N", async () => {
    const s = setup();
    await s.write("foo one\r\nbar Foo\r\nfoo two\r\n");
    enterChord(s);
    keys(s, "g");
    keys(s, "/");
    expect(s.copy.state().prompt).toEqual({ direction: "forward" });
    s.copy.submitSearch("foo");
    expect(s.copy.state().prompt).toBeNull();
    expect(s.copy.state().cursor).toEqual({ x: 4, y: 1 });
    keys(s, "n");
    expect(s.copy.state().cursor).toEqual({ x: 0, y: 2 });
    keys(s, "N");
    expect(s.copy.state().cursor).toEqual({ x: 4, y: 1 });
    keys(s, "?");
    s.copy.submitSearch("Foo");
    // A capital makes the search case sensitive; it wraps around to the only match.
    expect(s.copy.state().cursor).toEqual({ x: 4, y: 1 });
    s.copy.submitSearch("missing");
    expect(s.copy.state().message).toContain("missing");
  });

  it("keeps keys and pastes from the program while active and restores input on exit", async () => {
    const s = setup();
    await s.write("$ ");
    s.key("Enter");
    expect(s.sent).toEqual(["\r"]);

    enterChord(s);
    s.key("Enter");
    s.key("x");
    s.key("c", { ctrl: true });
    expect(s.copy.isActive()).toBe(false);
    enterChord(s);
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    s.term.textarea!.dispatchEvent(paste);
    expect(paste.defaultPrevented).toBe(true);
    s.key("Escape");
    expect(s.copy.isActive()).toBe(false);
    expect(s.sent).toEqual(["\r"]);

    s.key("Enter");
    expect(s.sent).toEqual(["\r", "\r"]);
  });

  it("toggles off with the same chord and stays off where it is unavailable", async () => {
    const s = setup({ available: false });
    enterChord(s);
    expect(s.copy.isActive()).toBe(false);
    const t = setup();
    enterChord(t);
    expect(t.copy.isActive()).toBe(true);
    enterChord(t);
    expect(t.copy.isActive()).toBe(false);
  });

  it("scrolls the viewport to follow the cursor through scrollback", async () => {
    const s = setup({ rows: 4 });
    await s.write(Array.from({ length: 20 }, (_, i) => `line ${i}`).join("\r\n"));
    // jsdom has no layout, so xterm cannot scroll by pixels here: record the scroll requests instead.
    const toLine = vi.spyOn(s.term, "scrollToLine");
    const byLines = vi.spyOn(s.term, "scrollLines");
    const toBottom = vi.spyOn(s.term, "scrollToBottom");
    enterChord(s);
    const last = s.term.buffer.active.length - 1;
    expect(s.copy.state().cursor.y).toBe(last);
    keys(s, "g");
    expect(toLine).toHaveBeenLastCalledWith(0);
    s.key("d", { ctrl: true });
    expect(byLines).toHaveBeenLastCalledWith(2);
    expect(s.copy.state().cursor.y).toBe(2);
    s.key("PageDown");
    expect(byLines).toHaveBeenLastCalledWith(4);
    expect(s.copy.state().cursor.y).toBe(6);
    keys(s, "q");
    expect(toBottom).toHaveBeenCalled();
  });
  it("leaves copy mode when the program resets the terminal", async () => {
    const s = setup();
    await s.write("hello\r\n$ ");
    enterChord(s);
    expect(s.copy.isActive()).toBe(true);
    await s.write("\x1bc");
    expect(s.copy.isActive()).toBe(false);
  });

  it("treats a printable AltGr character as a plain key", async () => {
    const s = setup();
    await s.write("abc def\r\n$ ");
    enterChord(s);
    keys(s, "k0");
    const altGr = new KeyboardEvent("keydown", { key: "$", code: "Digit4", ctrlKey: true, altKey: true, bubbles: true, cancelable: true });
    Object.defineProperty(altGr, "getModifierState", { value: (name: string) => name === "AltGraph" });
    s.term.textarea!.dispatchEvent(altGr);
    // "$" moves to the end of the line like the plain key.
    expect(s.copy.state().cursor.x).toBe(6);
    expect(s.copy.isActive()).toBe(true);
  });
});
