/** @vitest-environment jsdom */

// The WebKit composition-commit Enter rule, driven with a fixed clock so the 50 ms window is deterministic.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";

vi.mock("../../ipc/env", () => ({ WEB: false }));

import { attachTerminalInput, isWebKitEngine } from "../../features/terminal/terminalInput/attachTerminalInput";
import { COMPOSITION_COMMIT_WINDOW_MS } from "../../features/terminal/terminalInput/resolveKey";

let cleanup: (() => void) | null = null;

function setup(webkitCommitEnter: boolean) {
  let clock = 1000;
  const host = document.createElement("div");
  document.body.append(host);
  const term = new Terminal();
  term.open(host);
  const detach = attachTerminalInput(term, host, { kittyKeyboard: false, webkitCommitEnter, now: () => clock });
  const sent: string[] = [];
  // xterm reports an empty string for a compositionend without a composition; only real bytes matter here.
  term.onData((data) => {
    if (data) sent.push(data);
  });
  const textarea = term.textarea!;
  const enter = () => {
    const init = { key: "Enter", code: "Enter", keyCode: 13, bubbles: true, cancelable: true };
    const down = new KeyboardEvent("keydown", init);
    textarea.dispatchEvent(down);
    if (!down.defaultPrevented) textarea.dispatchEvent(new KeyboardEvent("keypress", { ...init, charCode: 13 }));
    textarea.dispatchEvent(new KeyboardEvent("keyup", init));
  };
  const compositionEnd = () => textarea.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "" }));
  cleanup = () => {
    detach();
    term.dispose();
    host.remove();
  };
  return {
    sent,
    enter,
    compositionEnd,
    advance: (ms: number) => {
      clock += ms;
    },
  };
}

beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
});

afterEach(() => {
  cleanup?.();
  cleanup = null;
  vi.unstubAllGlobals();
});

describe("WebKit composition-commit Enter", () => {
  it("swallows the committing Enter and its keypress, then lets the next Enter inside the window submit", () => {
    const view = setup(true);
    view.compositionEnd();
    view.enter();
    expect(view.sent).toEqual([]);
    view.enter();
    expect(view.sent).toEqual(["\r"]);
  });

  it("lets an Enter through once the window has passed", () => {
    const view = setup(true);
    view.compositionEnd();
    view.advance(COMPOSITION_COMMIT_WINDOW_MS + 1);
    view.enter();
    expect(view.sent).toEqual(["\r"]);
  });

  it("is consumed by any other key first", () => {
    const view = setup(true);
    view.compositionEnd();
    const textarea = document.querySelector<HTMLTextAreaElement>("textarea.xterm-helper-textarea")!;
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Shift", code: "ShiftLeft", keyCode: 16, shiftKey: true, bubbles: true }));
    view.enter();
    expect(view.sent).toEqual(["\r"]);
  });

  it("never applies on Chromium-based engines", () => {
    const view = setup(false);
    view.compositionEnd();
    view.enter();
    expect(view.sent).toEqual(["\r"]);
  });

  it.each([
    ["macOS Safari", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", true],
    ["WKWebView", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko)", true],
    ["iOS Chrome (WebKit)", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0 Mobile/15E148 Safari/604.1", true],
    ["WebKitGTK", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", true],
    ["Chrome", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", false],
    ["WebView2 / Edge", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", false],
    ["Android WebView", "Mozilla/5.0 (Linux; Android 14; Pixel 8; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.0.0 Mobile Safari/537.36", false],
    ["Firefox", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0", false],
  ])("detects %s as WebKit: %s", (_name, userAgent, expected) => {
    expect(isWebKitEngine(userAgent)).toBe(expected);
  });
});
