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
  it("writeText 成功时不走 wails 与 execCommand", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard(writeText);
    await copyText("hello");
    expect(writeText).toHaveBeenCalledWith("hello");
    expect(mocks.setText).not.toHaveBeenCalled();
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("writeText 拒绝时桌面端走 wails SetText", async () => {
    mocks.env.WEB = false;
    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    await copyText("hello");
    expect(mocks.setText).toHaveBeenCalledWith("hello");
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("剪贴板 API 缺失时桌面端直接走 wails", async () => {
    mocks.env.WEB = false;
    stubClipboard(undefined);
    await copyText("hello");
    expect(mocks.setText).toHaveBeenCalledWith("hello");
    expect(mocks.execCommand).not.toHaveBeenCalled();
  });

  it("writeText 与 wails 都失败时走 execCommand 兜底并清理临时元素", async () => {
    mocks.env.WEB = false;
    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    mocks.setText.mockRejectedValue(new Error("wails down"));
    await copyText("hello");
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("web 环境跳过 wails 直接 execCommand 兜底", async () => {
    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    await copyText("hello");
    expect(mocks.setText).not.toHaveBeenCalled();
    expect(mocks.execCommand).toHaveBeenCalledWith("copy");
  });

  it("全部失败时抛出首个错误", async () => {
    mocks.env.WEB = false;
    const denied = new Error("denied");
    stubClipboard(vi.fn().mockRejectedValue(denied));
    mocks.setText.mockRejectedValue(new Error("wails down"));
    mocks.execCommand.mockReturnValue(false);
    await expect(copyText("hello")).rejects.toBe(denied);
  });

  it("全链路不可用（execCommand 缺失）时抛错", async () => {
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
  it("readText 可用时直接返回，不走 wails", async () => {
    const navigatorRead = vi.fn().mockResolvedValue("hello");
    stubClipboardReader(navigatorRead);
    await expect(readText()).resolves.toBe("hello");
    expect(navigatorRead).toHaveBeenCalledTimes(1);
    expect(mocks.text).not.toHaveBeenCalled();
  });

  it("剪贴板 API 缺失时桌面端走 wails Text", async () => {
    mocks.env.WEB = false;
    stubClipboardReader(undefined);
    await expect(readText()).resolves.toBe("wails-text");
    expect(mocks.text).toHaveBeenCalledTimes(1);
  });

  it("readText 拒绝时桌面端回退 wails Text", async () => {
    mocks.env.WEB = false;
    stubClipboardReader(vi.fn().mockRejectedValue(new Error("denied")));
    await expect(readText()).resolves.toBe("wails-text");
    expect(mocks.text).toHaveBeenCalledTimes(1);
  });

  it("web 环境不走 wails，原样抛出错误", async () => {
    const denied = new Error("denied");
    stubClipboardReader(vi.fn().mockRejectedValue(denied));
    await expect(readText()).rejects.toBe(denied);
    expect(mocks.text).not.toHaveBeenCalled();
  });

  it("readText 与 wails 都失败时抛出首个错误", async () => {
    mocks.env.WEB = false;
    const denied = new Error("denied");
    stubClipboardReader(vi.fn().mockRejectedValue(denied));
    mocks.text.mockRejectedValue(new Error("wails down"));
    await expect(readText()).rejects.toBe(denied);
  });

  it("web 环境剪贴板 API 缺失时抛错", async () => {
    stubClipboardReader(undefined);
    await expect(readText()).rejects.toThrow("剪贴板不可用");
    expect(mocks.text).not.toHaveBeenCalled();
  });
});
