/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({ copyText: vi.fn(), toast: vi.fn() }));

vi.mock("../../ui/clipboard", () => ({ copyText: harness.copyText, readText: vi.fn() }));

import { TerminalLinkPanel } from "../../features/terminal/TerminalLinkPanel";
import { registerTerminalIntegrations } from "../../features/terminal/terminalIntegrations";
import { useUi } from "../../app/store";

const LINKS = [
  { url: "https://new.example.com/a", context: "latest line", row: 30 },
  { url: "https://mid.example.com/b", context: "middle line", row: 20 },
  { url: "https://old.example.com/c", context: "oldest line", row: 10 },
];

let view: MountedView | null = null;
let restore: (() => void) | null = null;

function render(props: Partial<Parameters<typeof TerminalLinkPanel>[0]> = {}) {
  const onOpen = vi.fn();
  const onClose = vi.fn();
  view = mount(createElement(TerminalLinkPanel, { links: LINKS, onOpen, onClose, ...props }));
  return { onOpen, onClose };
}

function key(init: KeyboardEventInit & { key: string }) {
  const list = document.querySelector('[role="listbox"]') as HTMLElement;
  act(() => {
    list.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));
  });
}

function selected(): string {
  return document.querySelector('[role="option"][aria-selected="true"]')?.textContent ?? "";
}

beforeEach(() => {
  harness.copyText.mockReset().mockResolvedValue(undefined);
  harness.toast.mockReset();
  useUi.setState({ pushToast: harness.toast } as never);
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  view?.unmount();
  view = null;
  restore?.();
  restore = null;
});

describe("TerminalLinkPanel", () => {
  it("lists links in the given order with their context", () => {
    render();
    const options = [...document.querySelectorAll('[role="option"]')];
    expect(options).toHaveLength(3);
    expect(options[0].textContent).toContain("new.example.com");
    expect(options[0].textContent).toContain("latest line");
    expect(options[0].getAttribute("aria-selected")).toBe("true");
  });

  it("moves the selection with the arrow keys", () => {
    render();
    key({ key: "ArrowDown" });
    expect(selected()).toContain("mid.example.com");
    key({ key: "ArrowDown" });
    key({ key: "ArrowDown" });
    expect(selected()).toContain("old.example.com");
    key({ key: "ArrowUp" });
    expect(selected()).toContain("mid.example.com");
  });

  it("opens the selected link on Enter", () => {
    const { onOpen } = render();
    key({ key: "ArrowDown" });
    key({ key: "Enter" });
    expect(onOpen).toHaveBeenCalledWith("https://mid.example.com/b");
  });

  it("opens a link on click", () => {
    const { onOpen } = render();
    const options = document.querySelectorAll('[role="option"]');
    act(() => (options[2] as HTMLElement).click());
    expect(onOpen).toHaveBeenCalledWith("https://old.example.com/c");
  });

  it("copies the selected link on C and reports it", async () => {
    render();
    key({ key: "c" });
    await flush();
    expect(harness.copyText).toHaveBeenCalledWith("https://new.example.com/a");
    expect(harness.toast).toHaveBeenCalledWith("success", "已复制链接");
  });

  it("reports a failed copy", async () => {
    harness.copyText.mockRejectedValue(new Error("denied"));
    render();
    key({ key: "C" });
    await flush();
    expect(harness.toast).toHaveBeenCalledWith("error", expect.stringContaining("复制链接失败"));
  });

  it("closes on Escape", () => {
    const { onClose } = render();
    key({ key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("ignores Shift+Enter and hides its hint while no in-app opener is registered", () => {
    const { onOpen } = render();
    key({ key: "Enter", shiftKey: true });
    expect(onOpen).not.toHaveBeenCalled();
    expect(document.body.textContent).not.toContain("在 NexTerm 中打开");
    expect(document.body.textContent).toContain("Enter 打开");
  });

  it("opens in the app on Shift+Enter once an opener is registered", () => {
    const openInApp = vi.fn();
    restore = registerTerminalIntegrations({ openInApp });
    const { onOpen, onClose } = render();
    expect(document.body.textContent).toContain("Shift+Enter 在 NexTerm 中打开");
    key({ key: "Enter", shiftKey: true });
    expect(openInApp).toHaveBeenCalledWith("https://new.example.com/a");
    expect(onOpen).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it("passes only the planned http(s) URL to the in-app opener", () => {
    const openInApp = vi.fn();
    restore = registerTerminalIntegrations({ openInApp });
    render({ links: [{ url: "https://Example.com", context: "", row: 1 }] });
    key({ key: "Enter", shiftKey: true });
    expect(openInApp).toHaveBeenCalledWith("https://example.com/");
  });

  it.each(["mailto:a@example.com", "javascript:alert(1)"])("refuses %s in the app and says so", (url) => {
    const openInApp = vi.fn();
    restore = registerTerminalIntegrations({ openInApp });
    render({ links: [{ url, context: "", row: 1 }] });
    key({ key: "Enter", shiftKey: true });
    expect(openInApp).not.toHaveBeenCalled();
    expect(harness.toast).toHaveBeenCalledWith("info", expect.stringContaining("http/https"));
  });

  it("shows the context without control or invisible characters", () => {
    render({ links: [{ url: "https://a.example.com/", context: "ok\u202e\u200b\u0007 line", row: 1 }] });
    const ctx = document.querySelector(".nx-term-link-context")?.textContent;
    expect(ctx).toBe("ok line");
  });

  it("explains an empty list", () => {
    render({ links: [] });
    expect(document.body.textContent).toContain("当前终端没有可识别的链接");
    expect(document.querySelectorAll('[role="option"]')).toHaveLength(0);
  });
});
