/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  setText: vi.fn(),
  text: vi.fn(),
  execCommand: vi.fn(),
  env: { clientId: () => "test-client", WEB: true },
}));

vi.mock("@wailsio/runtime", () => ({
  Clipboard: { SetText: mocks.setText, Text: mocks.text },
}));

vi.mock("../../ipc/env", () => mocks.env);

import { copyText, readText } from "../../ui/clipboard";

function stubClipboard(writeText?: (text: string) => Promise<void>): void {
  Object.defineProperty(navigator, "clipboard", {
    value: writeText ? { writeText } : undefined,
    configurable: true,
  });
}

function stubClipboardReader(readTextFn?: () => Promise<string>): void {
  Object.defineProperty(navigator, "clipboard", {
    value: readTextFn ? { readText: readTextFn } : undefined,
    configurable: true,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.env.WEB = true;
  mocks.setText.mockResolvedValue(undefined);
  mocks.text.mockResolvedValue("wails-text");
  mocks.execCommand.mockReturnValue(true);
  Object.defineProperty(document, "execCommand", {
    value: mocks.execCommand,
    configurable: true,
    writable: true,
  });
});

afterEach(() => {
  Object.defineProperty(document, "execCommand", {
    value: undefined,
    configurable: true,
    writable: true,
  });
});

describe("copyText", () => {
  it("does not use wails or execCommand when writeText succeeds", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard(writeText);
    await copyText("hello");
    expect(writeText).toHaveBeenCalledWith("hello");
    expect(mocks.setText).not.toHaveBeenCalled();
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("verifies navigator writeText on desktop before returning", async () => {
    mocks.env.WEB = false;
    const writeText = vi.fn().mockResolvedValue(undefined);
    const navigatorRead = vi.fn().mockResolvedValue("hello");
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText, readText: navigatorRead },
      configurable: true,
    });
    await copyText("hello");
    expect(writeText).toHaveBeenCalledWith("hello");
    expect(navigatorRead).toHaveBeenCalledTimes(1);
    expect(mocks.setText).not.toHaveBeenCalled();
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("rejects when navigator writeText silently drops the write and every fallback fails", async () => {
    mocks.env.WEB = false;
    const writeText = vi.fn().mockResolvedValue(undefined);
    const navigatorRead = vi.fn().mockResolvedValue("");
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText, readText: navigatorRead },
      configurable: true,
    });
    mocks.text.mockResolvedValue("");
    mocks.execCommand.mockReturnValue(false);
    await expect(copyText("hello")).rejects.toThrow("剪贴板写入失败");
    expect(writeText).toHaveBeenCalledWith("hello");
    expect(navigatorRead).toHaveBeenCalledTimes(1);
    expect(mocks.setText).toHaveBeenCalledWith("hello");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
  });

  it("falls back to wails SetText on desktop when writeText rejects", async () => {
    mocks.env.WEB = false;
    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    mocks.text.mockResolvedValue("hello");
    await copyText("hello");
    expect(mocks.setText).toHaveBeenCalledWith("hello");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("uses wails directly on desktop when the clipboard API is missing", async () => {
    mocks.env.WEB = false;
    stubClipboard(undefined);
    mocks.text.mockResolvedValue("hello");
    await copyText("hello");
    expect(mocks.setText).toHaveBeenCalledWith("hello");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("rejects when wails SetText silently drops the write and every fallback fails", async () => {
    mocks.env.WEB = false;
    stubClipboard(undefined);
    mocks.text.mockResolvedValue("");
    mocks.execCommand.mockReturnValue(false);
    await expect(copyText("hello")).rejects.toThrow("剪贴板写入失败");
    expect(mocks.setText).toHaveBeenCalledWith("hello");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
  });

  it("falls back to execCommand and cleans up temporary elements when writeText and wails fail", async () => {
    mocks.env.WEB = false;
    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    mocks.setText.mockRejectedValue(new Error("wails down"));
    mocks.text.mockResolvedValue("hello");
    await copyText("hello");
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("rejects when execCommand reports a desktop copy but read-back is empty", async () => {
    mocks.env.WEB = false;
    stubClipboard(undefined);
    mocks.setText.mockRejectedValue(new Error("wails down"));
    mocks.text.mockResolvedValue("");
    await expect(copyText("hello")).rejects.toThrow("wails down");
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("preserves the first error when execCommand read-back rejects", async () => {
    mocks.env.WEB = false;
    const denied = new Error("denied");
    stubClipboard(vi.fn().mockRejectedValue(denied));
    mocks.setText.mockRejectedValue(new Error("wails down"));
    mocks.text.mockRejectedValue(new Error("read failed"));
    await expect(copyText("hello")).rejects.toBe(denied);
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
    expect(mocks.text).toHaveBeenCalledTimes(1);
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("skips wails and uses execCommand on web", async () => {
    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    await copyText("hello");
    expect(mocks.setText).not.toHaveBeenCalled();
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
  });

  it("throws the first error when all fallbacks fail", async () => {
    mocks.env.WEB = false;
    const denied = new Error("denied");
    stubClipboard(vi.fn().mockRejectedValue(denied));
    mocks.setText.mockRejectedValue(new Error("wails down"));
    mocks.execCommand.mockReturnValue(false);
    await expect(copyText("hello")).rejects.toBe(denied);
  });

  it("throws when the entire fallback chain is unavailable (execCommand missing)", async () => {
    stubClipboard(undefined);
    Object.defineProperty(document, "execCommand", {
      value: undefined,
      configurable: true,
      writable: true,
    });
    await expect(copyText("hello")).rejects.toThrow("剪贴板不可用");
  });
});

describe("readText", () => {
  it("returns navigator readText results directly without using wails", async () => {
    const navigatorRead = vi.fn().mockResolvedValue("hello");
    stubClipboardReader(navigatorRead);
    await expect(readText()).resolves.toBe("hello");
    expect(navigatorRead).toHaveBeenCalledTimes(1);
    expect(mocks.text).not.toHaveBeenCalled();
  });

  it("uses wails Text on desktop when the clipboard API is missing", async () => {
    mocks.env.WEB = false;
    stubClipboardReader(undefined);
    await expect(readText()).resolves.toBe("wails-text");
    expect(mocks.text).toHaveBeenCalledTimes(1);
  });

  it("falls back to wails Text on desktop when readText rejects", async () => {
    mocks.env.WEB = false;
    stubClipboardReader(vi.fn().mockRejectedValue(new Error("denied")));
    await expect(readText()).resolves.toBe("wails-text");
    expect(mocks.text).toHaveBeenCalledTimes(1);
  });

  it("rethrows the original error on web without using wails", async () => {
    const denied = new Error("denied");
    stubClipboardReader(vi.fn().mockRejectedValue(denied));
    await expect(readText()).rejects.toBe(denied);
    expect(mocks.text).not.toHaveBeenCalled();
  });

  it("throws the first error when readText and wails both fail", async () => {
    mocks.env.WEB = false;
    const denied = new Error("denied");
    stubClipboardReader(vi.fn().mockRejectedValue(denied));
    mocks.text.mockRejectedValue(new Error("wails down"));
    await expect(readText()).rejects.toBe(denied);
  });

  it("throws on web when the clipboard API is missing", async () => {
    stubClipboardReader(undefined);
    await expect(readText()).rejects.toThrow("剪贴板不可用");
    expect(mocks.text).not.toHaveBeenCalled();
  });
});
