/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  captureKeyboard,
  isKeyboardCaptured,
  isReleaseChord,
  releaseKeyboard,
  resetKeyboardCaptureForTest,
  subscribeKeyboardCapture,
} from "../../app/inputCapture";
import { useAppKeybindings, type AppKeybindingHandlers } from "../../app/shell/useAppKeybindings";
import { mount, type MountedView } from "./reactTestUtils";

function handlers(): AppKeybindingHandlers {
  return {
    leftDockOpen: false,
    rightDockOpen: false,
    activeTabId: null,
    setLeftOpen: vi.fn(),
    setRightOpen: vi.fn(),
    openLocalTerminal: vi.fn(async () => undefined),
    openNewTerminal: vi.fn(async () => undefined),
    lockVault: vi.fn(async () => undefined),
    openCommandPalette: vi.fn(),
    openQuickConnect: vi.fn(),
    openSettings: vi.fn(),
  };
}

function Harness({ value }: { value: AppKeybindingHandlers }) {
  useAppKeybindings(value);
  return null;
}

let view: MountedView | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
  resetKeyboardCaptureForTest();
});

function press(init: KeyboardEventInit): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  act(() => {
    window.dispatchEvent(event);
  });
  return event;
}

describe("keyboard capture", () => {
  it("is owned by one pane and released only by that pane", () => {
    captureKeyboard("tab-a");
    expect(isKeyboardCaptured()).toBe(true);
    releaseKeyboard("tab-b");
    expect(isKeyboardCaptured()).toBe(true);
    releaseKeyboard("tab-a");
    expect(isKeyboardCaptured()).toBe(false);
  });

  it("tells subscribers about changes", () => {
    const listener = vi.fn();
    const stop = subscribeKeyboardCapture(listener);
    captureKeyboard("tab-a");
    releaseKeyboard("tab-a");
    stop();
    captureKeyboard("tab-a");
    expect(listener).toHaveBeenCalledTimes(2);
  });

  it("recognizes only Ctrl+Alt+Shift+Esc as the release chord", () => {
    expect(isReleaseChord({ key: "Escape", ctrlKey: true, altKey: true, shiftKey: true })).toBe(true);
    expect(isReleaseChord({ key: "Escape", ctrlKey: true, altKey: false, shiftKey: true })).toBe(false);
    expect(isReleaseChord({ key: "a", ctrlKey: true, altKey: true, shiftKey: true })).toBe(false);
  });

  it("lets the app shortcuts run when nothing is captured", () => {
    const value = handlers();
    view = mount(createElement(Harness, { value }));
    press({ key: "P", ctrlKey: true, shiftKey: true });
    press({ key: "P", metaKey: true, shiftKey: true });
    expect(value.openCommandPalette).toHaveBeenCalled();
  });

  it("stands the app shortcuts down while the remote screen owns the keyboard", () => {
    const value = handlers();
    view = mount(createElement(Harness, { value }));
    captureKeyboard("tab-a");
    const event = press({ key: "P", ctrlKey: true, shiftKey: true });
    press({ key: "P", metaKey: true, shiftKey: true });
    expect(value.openCommandPalette).not.toHaveBeenCalled();
    // The shortcut is not swallowed either: the remote client decides what to do with the key.
    expect(event.defaultPrevented).toBe(false);
  });

  it("brings the shortcuts back after release", () => {
    const value = handlers();
    view = mount(createElement(Harness, { value }));
    captureKeyboard("tab-a");
    releaseKeyboard("tab-a");
    press({ key: "P", ctrlKey: true, shiftKey: true });
    press({ key: "P", metaKey: true, shiftKey: true });
    expect(value.openCommandPalette).toHaveBeenCalled();
  });
});
