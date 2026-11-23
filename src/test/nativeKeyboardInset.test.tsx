/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const native = vi.hoisted(() => ({
  handlers: new Map<string, (payload: unknown) => void>(),
  unlisten: vi.fn(),
}));

vi.mock("../ipc/env", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/env")>()),
  DESKTOP: true,
  WEB: false,
  TRANSPORT: "desktop",
}));
vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
  listenWailsEvent: vi.fn((event: string, handler: (payload: unknown) => void) => {
    native.handlers.set(event, handler);
    return Promise.resolve(native.unlisten);
  }),
}));

import {
  NATIVE_KEYBOARD_EVENT,
  nativeKeyboardAvailable,
  nativeKeyboardInset,
  useKeyboardInset,
} from "../app/shell/keyboardInset";

describe("native keyboard inset", () => {
  it("reads the Android bridge payload as an object or JSON text", () => {
    expect(nativeKeyboardInset({ visible: true, height: 300 }, 800)).toBe(300);
    expect(nativeKeyboardInset('{"visible":true,"height":280}', 800)).toBe(280);
    expect(nativeKeyboardInset({ visible: false, height: 300 }, 800)).toBe(0);
    expect(nativeKeyboardInset({ visible: true, height: 0 }, 800)).toBe(0);
  });

  it("ignores a payload it cannot trust, so the browser readings stay in use", () => {
    // Device pixels instead of CSS px on a 3x screen: taller than the page.
    expect(nativeKeyboardInset({ visible: true, height: 900 }, 800)).toBeNull();
    expect(nativeKeyboardInset({ visible: true, height: 700 }, 800)).toBeNull();
    expect(nativeKeyboardInset({ visible: true, height: -1 }, 800)).toBeNull();
    expect(nativeKeyboardInset({ visible: true, height: "300" }, 800)).toBeNull();
    expect(nativeKeyboardInset({ visible: true, height: 300 }, Number.NaN)).toBeNull();
    expect(nativeKeyboardInset("not json", 800)).toBeNull();
    expect(nativeKeyboardInset(null, 800)).toBeNull();
  });

  it("is listened for only in the Android app", () => {
    const android = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/128.0 Mobile Safari/537.36";
    expect(nativeKeyboardAvailable(true, android)).toBe(true);
    expect(nativeKeyboardAvailable(false, android)).toBe(false);
    expect(nativeKeyboardAvailable(true, "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")).toBe(false);
  });
});

describe("useKeyboardInset with the Android bridge", () => {
  const root = document.documentElement;
  let view: MountedView | null = null;

  function Probe() {
    useKeyboardInset();
    return null;
  }

  async function frames(): Promise<void> {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  beforeEach(() => {
    native.handlers.clear();
    native.unlisten.mockClear();
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/128.0 Mobile Safari/537.36",
    );
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800);
    // A frame runs on the next macrotask, which frames() waits for.
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0) as unknown as number);
    vi.stubGlobal("cancelAnimationFrame", (id: number) => clearTimeout(id));
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("sizes the visible area from the native keyboard height and closes it when the keyboard hides", async () => {
    view = mount(createElement(Probe));
    await flush();
    await frames();
    expect(root.dataset.nxKeyboard).toBeUndefined();
    const emit = native.handlers.get(NATIVE_KEYBOARD_EVENT);
    expect(emit).toBeDefined();

    act(() => emit?.({ visible: true, height: 320 }));
    await frames();
    expect(root.style.getPropertyValue("--nx-kb-inset")).toBe("320px");
    expect(root.style.getPropertyValue("--nx-vv-top")).toBe("0px");
    expect(root.style.getPropertyValue("--nx-vv-height")).toBe("480px");
    expect(root.dataset.nxKeyboard).toBe("open");

    // An implausible report falls back to the browser's own readings, which see no keyboard here.
    act(() => emit?.({ visible: true, height: 2400 }));
    await frames();
    expect(root.style.getPropertyValue("--nx-kb-inset")).toBe("0px");
    expect(root.dataset.nxKeyboard).toBeUndefined();

    act(() => emit?.({ visible: true, height: 300 }));
    await frames();
    expect(root.style.getPropertyValue("--nx-kb-inset")).toBe("300px");

    act(() => emit?.({ visible: false, height: 0 }));
    await frames();
    expect(root.style.getPropertyValue("--nx-kb-inset")).toBe("0px");
    expect(root.dataset.nxKeyboard).toBeUndefined();

    view.unmount();
    view = null;
    expect(native.unlisten).toHaveBeenCalled();
  });

  it("keeps the visual viewport reading while the native report says the keyboard is closed", async () => {
    const vv = Object.assign(new EventTarget(), { height: 500, offsetTop: 0 });
    vi.stubGlobal("visualViewport", vv);
    view = mount(createElement(Probe));
    await flush();
    act(() => native.handlers.get(NATIVE_KEYBOARD_EVENT)?.({ visible: false, height: 0 }));
    await frames();
    // Where the browser sees the keyboard itself, its reading stays in use.
    expect(root.style.getPropertyValue("--nx-kb-inset")).toBe("300px");
    expect(root.dataset.nxKeyboard).toBe("open");
  });
});
