/** @vitest-environment jsdom */
import { describe, expect, it, vi } from "vitest";
import { NATIVE_PASTE_WAIT_MS, RemoteKeyboard, type RemoteKeyboardOptions } from "../../features/remote/remoteKeyboard";
import type { ClipboardOutcome } from "../../features/remote/remoteTypes";

function key(type: "keydown" | "keyup", code: string, init: KeyboardEventInit = {}): KeyboardEvent {
  return new KeyboardEvent(type, { code, ...init });
}

function setup(options: Partial<RemoteKeyboardOptions> = {}, sendClipboard = vi.fn(async (): Promise<ClipboardOutcome> => ({ status: "done" }))) {
  const log: string[] = [];
  const target = {
    sendKey: vi.fn((code: string, down: boolean) => log.push(`${code}:${down ? "down" : "up"}`)),
    sendClipboard: vi.fn(async () => {
      log.push("clipboard");
      return sendClipboard();
    }),
  };
  const opts: RemoteKeyboardOptions = { commandAsCtrl: true, syncPaste: true, pasteDelayMs: 0, ...options };
  const keyboard = new RemoteKeyboard(() => target, () => opts);
  return { keyboard, target, log };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

describe("RemoteKeyboard", () => {
  it("sends Command as Ctrl and consumes the Command key", () => {
    const { keyboard, log } = setup();
    expect(keyboard.handle(key("keydown", "MetaLeft", { metaKey: true }))).toBe(true);
    expect(keyboard.handle(key("keydown", "MetaLeft", { metaKey: true, repeat: true }))).toBe(true);
    expect(keyboard.handle(key("keyup", "MetaRight"))).toBe(true);
    expect(keyboard.handle(key("keyup", "MetaLeft"))).toBe(true);
    expect(log).toEqual(["ControlLeft:down", "ControlRight:up", "ControlLeft:up"]);
  });

  it("leaves Command alone when the mapping is off", () => {
    const { keyboard, log } = setup({ commandAsCtrl: false });
    expect(keyboard.handle(key("keydown", "MetaLeft", { metaKey: true }))).toBe(false);
    expect(keyboard.handle(key("keyup", "MetaLeft"))).toBe(false);
    expect(log).toEqual([]);
  });

  it("releases keys pressed with Command when Command goes up, since macOS sends no keyup for them", () => {
    const { keyboard, log } = setup();
    keyboard.handle(key("keydown", "MetaLeft", { metaKey: true }));
    expect(keyboard.handle(key("keydown", "KeyC", { metaKey: true }))).toBe(false);
    expect(keyboard.handle(key("keydown", "KeyX", { metaKey: true }))).toBe(false);
    expect(keyboard.handle(key("keyup", "KeyX", { metaKey: true }))).toBe(false);
    keyboard.handle(key("keyup", "MetaLeft"));
    expect(log).toEqual(["ControlLeft:down", "KeyC:up", "ControlLeft:up"]);
  });

  it("sends the local clipboard before the paste keys", async () => {
    const { keyboard, log } = setup();
    keyboard.handle(key("keydown", "MetaLeft", { metaKey: true }));
    expect(keyboard.handle(key("keydown", "KeyV", { metaKey: true }))).toBe(true);
    await settle();
    expect(log).toEqual(["ControlLeft:down", "clipboard", "KeyV:down", "KeyV:up"]);
  });

  it("keeps the modifier on the paste when it was let go while the clipboard was read", async () => {
    let finish: (outcome: ClipboardOutcome) => void = () => undefined;
    const { keyboard, log } = setup({}, vi.fn(() => new Promise<ClipboardOutcome>((resolve) => (finish = resolve))));
    keyboard.handle(key("keydown", "ControlLeft", { ctrlKey: true }));
    expect(keyboard.handle(key("keydown", "KeyV", { ctrlKey: true }))).toBe(true);
    // A second press while the first paste is in flight sends nothing more.
    expect(keyboard.handle(key("keydown", "KeyV", { ctrlKey: true }))).toBe(true);
    keyboard.handle(key("keyup", "ControlLeft"));
    // An empty local clipboard still lets the remote side paste its own.
    finish({ status: "empty" });
    await settle();
    expect(log).toEqual(["clipboard", "ControlLeft:down", "KeyV:down", "KeyV:up", "ControlLeft:up"]);
  });

  it("pastes with Command when Command is not mapped", async () => {
    const { keyboard, log } = setup({ commandAsCtrl: false });
    keyboard.handle(key("keydown", "MetaLeft", { metaKey: true }));
    expect(keyboard.handle(key("keydown", "KeyV", { metaKey: true }))).toBe(true);
    await settle();
    expect(log).toEqual(["clipboard", "KeyV:down", "KeyV:up"]);
  });

  it("passes paste through when clipboard sync is off or the chord has other modifiers", () => {
    const off = setup({ syncPaste: false });
    expect(off.keyboard.handle(key("keydown", "KeyV", { ctrlKey: true }))).toBe(false);
    expect(off.keyboard.handlePaste(null)).toBe(false);
    const on = setup();
    expect(on.keyboard.handle(key("keydown", "KeyV", { ctrlKey: true, shiftKey: true }))).toBe(false);
    expect(on.keyboard.handle(key("keydown", "KeyV"))).toBe(false);
    expect(on.target.sendClipboard).not.toHaveBeenCalled();
  });

  it("hands the native paste DataTransfer directly to sendClipboard and cancels the fallback timer", async () => {
    const log: string[] = [];
    const target = {
      sendKey: vi.fn((code: string, down: boolean) => log.push(`${code}:${down ? "down" : "up"}`)),
      sendClipboard: vi.fn(async (data?: DataTransfer | null) => {
        log.push(`clipboard:${data ? "event" : "fallback"}`);
        return { status: "done" } as ClipboardOutcome;
      }),
    };
    const keyboard = new RemoteKeyboard(
      () => target,
      () => ({ commandAsCtrl: true, syncPaste: true, pasteDelayMs: 0 }),
      () => true,
    );
    keyboard.handle(key("keydown", "MetaLeft", { metaKey: true }));
    expect(keyboard.handle(key("keydown", "KeyV", { metaKey: true }))).toBe(true);
    const fakeData = { getData: () => "pasted" } as unknown as DataTransfer;
    expect(keyboard.handlePaste(fakeData)).toBe(true);
    await settle();
    expect(log).toEqual(["ControlLeft:down", "clipboard:event", "KeyV:down", "KeyV:up"]);
    expect(target.sendClipboard).toHaveBeenCalledWith(fakeData);
  });

  it("does not paste the remote clipboard over local content the session cannot carry, and reports it", async () => {
    const outcomes: ClipboardOutcome[] = [];
    const log: string[] = [];
    const target = {
      sendKey: vi.fn((code: string, down: boolean) => log.push(`${code}:${down ? "down" : "up"}`)),
      sendClipboard: vi.fn(async (): Promise<ClipboardOutcome> => ({ status: "unsupported", note: "text only" })),
    };
    const keyboard = new RemoteKeyboard(
      () => target,
      () => ({ commandAsCtrl: false, syncPaste: true, pasteDelayMs: 0 }),
      undefined,
      (outcome) => outcomes.push(outcome),
    );
    expect(keyboard.handle(key("keydown", "KeyV", { ctrlKey: true }))).toBe(true);
    await settle();
    expect(log).toEqual([]);
    expect(outcomes).toEqual([{ status: "unsupported", note: "text only" }]);
  });

  it("presses no paste keys for a drop that delivered nothing", async () => {
    const { keyboard, log } = setup({}, vi.fn(async (): Promise<ClipboardOutcome> => ({ status: "empty" })));
    expect(keyboard.handlePaste({ getData: () => "" } as unknown as DataTransfer, "drop")).toBe(true);
    await settle();
    expect(log).toEqual(["clipboard"]);
  });

  it("waits for the late native paste event before reading the clipboard itself, then refocuses the remote", async () => {
    vi.useFakeTimers();
    try {
      const log: string[] = [];
      const target = {
        sendKey: vi.fn((code: string, down: boolean) => log.push(`${code}:${down ? "down" : "up"}`)),
        sendClipboard: vi.fn(async (data?: DataTransfer | null): Promise<ClipboardOutcome> => {
          log.push(`clipboard:${data ? "event" : "fallback"}`);
          return { status: "done" };
        }),
        focus: vi.fn(() => log.push("focus")),
      };
      const keyboard = new RemoteKeyboard(() => target, () => ({ commandAsCtrl: false, syncPaste: true, pasteDelayMs: 0 }), () => true);
      expect(keyboard.handle(key("keydown", "KeyV", { ctrlKey: true }))).toBe(true);
      await vi.advanceTimersByTimeAsync(NATIVE_PASTE_WAIT_MS - 50);
      expect(target.sendClipboard).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(100);
      expect(log).toEqual(["focus", "clipboard:fallback", "ControlLeft:down", "KeyV:down", "KeyV:up", "ControlLeft:up"]);
    } finally {
      vi.useRealTimers();
    }
  });
});
