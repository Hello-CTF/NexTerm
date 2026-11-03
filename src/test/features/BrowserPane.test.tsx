/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  embedCheck: vi.fn(),
  openExternalURL: vi.fn(),
  pushToast: vi.fn(),
  copyText: vi.fn(),
  web: false,
}));

vi.mock("../../ipc/previewApi", () => ({ previewApi: { embedCheck: mocks.embedCheck } }));
vi.mock("../../ipc/wails", () => ({ openExternalURL: mocks.openExternalURL }));
vi.mock("../../ipc/env", () => ({
  get WEB() {
    return mocks.web;
  },
  DESKTOP: true,
  TRANSPORT: "desktop",
}));
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText }));
vi.mock("../../app/shell/shellMode", () => ({ isMobileShell: () => false }));
vi.mock("../../app/store", () => ({
  useUi: Object.assign(() => undefined, { getState: () => ({ pushToast: mocks.pushToast }) }),
}));

import { BrowserPane } from "../../features/browser/BrowserPane";
import { mount, type MountedView } from "./reactTestUtils";

let view: MountedView | undefined;
const render = (url: string) => {
  view = mount(createElement(BrowserPane, { url }));
  return { container: view.container };
};
const text = () => view?.container.textContent ?? "";
const button = (name: string) =>
  Array.from(view!.container.querySelectorAll("button")).find((b) => b.textContent === name)!;
const fireLoad = (frame: Element) => act(() => void frame.dispatchEvent(new Event("load")));

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe("BrowserPane", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.embedCheck.mockReset().mockResolvedValue({ status: "embeddable" });
    mocks.openExternalURL.mockReset().mockResolvedValue(undefined);
    mocks.pushToast.mockReset();
    mocks.copyText.mockReset().mockResolvedValue(undefined);
    mocks.web = false;
  });
  afterEach(() => {
    view?.unmount();
    view = undefined;
    vi.useRealTimers();
  });

  it("renders a restricted iframe without extra permissions", async () => {
    const { container } = render("https://example.com/a");
    await flush();
    const frame = container.querySelector("iframe")!;
    expect(frame.getAttribute("src")).toBe("https://example.com/a");
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(frame.hasAttribute("allow")).toBe(false);
    const tokens = (frame.getAttribute("sandbox") ?? "").split(/\s+/);
    expect(tokens).toContain("allow-scripts");
    for (const bad of ["allow-popups", "allow-top-navigation", "allow-modals"]) expect(tokens).not.toContain(bad);
  });

  it("does not render an iframe for a URL the policy refuses", async () => {
    const { container } = render("http://wails.localhost/");
    await flush();
    expect(container.querySelector("iframe")).toBeNull();
    expect(button("在系统浏览器打开")).toBeTruthy();
  });

  it("opens the system browser once when the site forbids embedding", async () => {
    mocks.embedCheck.mockResolvedValue({ status: "blocked", reason: "x-frame-options" });
    render("https://example.com/");
    await flush();
    expect(mocks.openExternalURL).toHaveBeenCalledTimes(1);
    expect(mocks.openExternalURL).toHaveBeenCalledWith("https://example.com/");
    expect(mocks.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("不允许嵌入"));
  });

  it("shows unreachable without opening the system browser", async () => {
    mocks.embedCheck.mockResolvedValue({ status: "unreachable", reason: "connection refused" });
    render("https://example.com/");
    await flush();
    expect(mocks.openExternalURL).not.toHaveBeenCalled();
    expect(text()).toContain("无法连接该地址");
    expect(text()).toContain("connection refused");
  });

  it("falls back once after ten seconds without a load event", async () => {
    render("https://example.com/");
    await flush();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(9_000);
    });
    expect(mocks.openExternalURL).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(mocks.openExternalURL).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(mocks.openExternalURL).toHaveBeenCalledTimes(1);
  });

  it("does not fall back when the page loads in time", async () => {
    const { container } = render("https://example.com/");
    await flush();
    fireLoad(container.querySelector("iframe")!);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    expect(mocks.openExternalURL).not.toHaveBeenCalled();
  });

  it("explains in-frame navigation it cannot follow on the second load", async () => {
    const { container } = render("https://example.com/");
    await flush();
    const frame = container.querySelector("iframe")!;
    fireLoad(frame);
    expect(text()).not.toContain("跨源页面无法读取当前地址");
    fireLoad(frame);
    expect(text()).toContain("页面已在框架内跳转");
    expect(view!.container.querySelector<HTMLInputElement>('input[aria-label="地址"]')!.value).toBe("https://example.com/");
  });

  it("copies the address and reports the result", async () => {
    render("https://example.com/");
    await flush();
    act(() => button("复制").click());
    await flush();
    expect(mocks.copyText).toHaveBeenCalledWith("https://example.com/");
    expect(mocks.pushToast).toHaveBeenCalledWith("success", expect.any(String));
  });

  it("warns in browser mode that a blank page may forbid embedding and offers the system browser", async () => {
    mocks.web = true;
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      const { container } = render("https://example.com/");
      await flush();
      fireLoad(container.querySelector("iframe")!);
      expect(text()).toContain("若页面空白，该网站可能不允许嵌入");
      const buttons = Array.from(container.querySelectorAll("button")).filter((b) => b.textContent === "在系统浏览器打开");
      expect(buttons.length).toBeGreaterThanOrEqual(2);
      act(() => buttons[buttons.length - 1].click());
      expect(openSpy).toHaveBeenCalledWith("https://example.com/", "_blank", "noopener,noreferrer");
    } finally {
      openSpy.mockRestore();
    }
  });

  it("does not show the blank-page warning on desktop", async () => {
    render("https://example.com/");
    await flush();
    expect(text()).not.toContain("若页面空白");
  });
});
