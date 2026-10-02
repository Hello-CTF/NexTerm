/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "../features/reactTestUtils";

vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import { DialogHost } from "../../ui/DialogHost";
import { PromptModal } from "../../ui/PromptModal";
import {
  ask,
  promptText,
  registerDialogHandlers,
  registerPromptHandler,
} from "../../ui/dialogs";
import { useUi } from "../../app/store";

function keyDown(
  target: EventTarget,
  key: string,
  init: KeyboardEventInit = {},
): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function findButton(container: ParentNode, text: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
}

function compose(target: Element, type: "compositionstart" | "compositionend"): void {
  act(() => {
    target.dispatchEvent(new CompositionEvent(type, { bubbles: true }));
  });
}

describe("shared dialog accessibility", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    document.body.replaceChildren();
    document.body.style.overflow = "";
    useUi.setState({ appDialog: null, appChoice: null, textPrompt: null, toasts: [] });
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    useUi.setState({ appDialog: null, appChoice: null, textPrompt: null, toasts: [] });
    document.body.style.overflow = "";
    document.body.replaceChildren();
  });

  it("labels and traps a warning dialog, makes the background inert, and restores focus", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.textContent = "打开确认框";
    trigger.focus();
    document.body.style.overflow = "scroll";
    const resolve = vi.fn();
    mounted = mount(createElement(DialogHost));

    act(() => {
      useUi.setState({
        appDialog: { kind: "ask", message: "删除这个文件？", level: "warning", resolve },
      });
    });

    const modal = mounted.container.querySelector<HTMLElement>('[role="alertdialog"]');
    if (!modal) throw new Error("Warning dialog not found");
    const cancel = findButton(mounted.container, "取消");
    const confirm = findButton(mounted.container, "确定");
    const title = document.getElementById(modal.getAttribute("aria-labelledby") ?? "");
    const description = document.getElementById(modal.getAttribute("aria-describedby") ?? "");

    expect(modal.getAttribute("aria-modal")).toBe("true");
    expect(title?.textContent).toBe("确认");
    expect(description?.textContent).toBe("删除这个文件？");
    expect(document.activeElement).toBe(cancel);
    expect(trigger.inert).toBe(true);
    expect(document.body.style.overflow).toBe("hidden");
    expect(modal.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");

    confirm.focus();
    keyDown(confirm, "Tab");
    expect(document.activeElement).toBe(cancel);
    keyDown(cancel, "Tab", { shiftKey: true });
    expect(document.activeElement).toBe(confirm);

    click(cancel);
    await flush();
    expect(resolve).toHaveBeenCalledExactlyOnceWith(false);
    expect(useUi.getState().appDialog).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(trigger.inert).toBe(false);
    expect(trigger.hasAttribute("inert")).toBe(false);
    expect(document.body.style.overflow).toBe("scroll");
  });

  it("lets only the top nested choice consume Escape before returning to the dialog", async () => {
    const dialogResolve = vi.fn();
    const choiceResolve = vi.fn();
    mounted = mount(createElement(DialogHost));
    act(() => {
      useUi.setState({
        appDialog: { kind: "ask", message: "底层确认", level: "info", resolve: dialogResolve },
      });
    });
    act(() => {
      useUi.setState({
        appChoice: {
          title: "终端仍在运行",
          message: "选择关闭方式",
          level: "warning",
          options: [
            { key: "detach", label: "后台继续运行", primary: true },
            { key: "kill", label: "结束进程", danger: true },
          ],
          resolve: choiceResolve,
        },
      });
    });

    expect(mounted.container.querySelectorAll(".nx-modal")).toHaveLength(1);
    expect(document.activeElement).toBe(findButton(mounted.container, "后台继续运行"));
    keyDown(window, "Escape");
    await flush();

    expect(choiceResolve).toHaveBeenCalledExactlyOnceWith(null);
    expect(dialogResolve).not.toHaveBeenCalled();
    expect(useUi.getState().appChoice).toBeNull();
    expect(useUi.getState().appDialog?.message).toBe("底层确认");
    expect(mounted.container.textContent).toContain("底层确认");
    expect(document.activeElement).toBe(findButton(mounted.container, "确定"));

    keyDown(window, "Escape");
    expect(dialogResolve).toHaveBeenCalledExactlyOnceWith(false);
    expect(useUi.getState().appDialog).toBeNull();
  });

  it("clears the old dialog before a resolver opens its replacement", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.focus();
    const replacementResolve = vi.fn();
    const firstResolve = vi.fn(() => {
      useUi.setState({
        appDialog: {
          kind: "message",
          message: "第二阶段消息",
          level: "info",
          resolve: replacementResolve,
        },
      });
    });
    mounted = mount(createElement(DialogHost));
    act(() => {
      useUi.setState({
        appDialog: { kind: "ask", message: "第一阶段", level: "warning", resolve: firstResolve },
      });
    });

    clickButton(mounted.container, "取消");

    expect(firstResolve).toHaveBeenCalledExactlyOnceWith(false);
    expect(useUi.getState().appDialog?.message).toBe("第二阶段消息");
    expect(mounted.container.textContent).toContain("第二阶段消息");
    clickButton(mounted.container, "确定");
    expect(replacementResolve).toHaveBeenCalledExactlyOnceWith(true);
    expect(useUi.getState().appDialog).toBeNull();
    await flush();
    expect(document.activeElement).toBe(trigger);
  });

  it("initializes a prompt that is already active when mounted", () => {
    const resolve = vi.fn();
    useUi.setState({
      textPrompt: { message: "输入名称", value: "ready", resolve },
    });
    mounted = mount(createElement(PromptModal));
    const input = mounted.container.querySelector("input");
    if (!input) throw new Error("Prompt input not found");

    expect(input.value).toBe("ready");
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe(5);
    keyDown(input, "Enter");
    expect(resolve).toHaveBeenCalledExactlyOnceWith("ready");
  });

  it("selects the supplied value and does not submit for IME composition or repeated Enter", () => {
    const resolve = vi.fn();
    mounted = mount(createElement(PromptModal));
    act(() => {
      useUi.setState({
        textPrompt: { message: "输入名称", value: "seed", resolve },
      });
    });
    const input = mounted.container.querySelector("input");
    if (!input) throw new Error("Prompt input not found");

    expect(document.activeElement).toBe(input);
    expect(input.value).toBe("seed");
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe(4);
    setInputValue(input, "汉字");
    compose(input, "compositionstart");
    keyDown(input, "Enter", { isComposing: true });
    expect(resolve).not.toHaveBeenCalled();
    expect(useUi.getState().textPrompt).not.toBeNull();

    compose(input, "compositionend");
    keyDown(input, "Enter", { repeat: true });
    expect(resolve).not.toHaveBeenCalled();
    keyDown(input, "Enter");
    expect(resolve).toHaveBeenCalledExactlyOnceWith("汉字");
    expect(useUi.getState().textPrompt).toBeNull();
  });

  it("cancels a prompt with Escape from a footer button", () => {
    const resolve = vi.fn();
    mounted = mount(createElement(PromptModal));
    act(() => {
      useUi.setState({
        textPrompt: { message: "输入名称", value: "seed", resolve },
      });
    });
    const cancel = findButton(mounted.container, "取消");
    cancel.focus();
    keyDown(cancel, "Escape");
    expect(resolve).toHaveBeenCalledExactlyOnceWith(null);
    expect(useUi.getState().textPrompt).toBeNull();
  });

  it("keeps multiline Enter for text and submits only the platform modifier shortcut", () => {
    const resolve = vi.fn();
    mounted = mount(createElement(PromptModal));
    act(() => {
      useUi.setState({
        textPrompt: { message: "任务描述", value: "", multiLine: true, resolve },
      });
    });
    const input = mounted.container.querySelector("textarea");
    if (!input) throw new Error("Prompt textarea not found");

    setInputValue(input, "第一行");
    keyDown(input, "Enter");
    expect(resolve).not.toHaveBeenCalled();
    keyDown(input, "Enter", { ctrlKey: true, repeat: true });
    expect(resolve).not.toHaveBeenCalled();
    keyDown(input, "Enter", { ctrlKey: true });
    expect(resolve).toHaveBeenCalledExactlyOnceWith("第一行");
  });

  it("announces errors and statuses without making the live regions inert", () => {
    mounted = mount(createElement(DialogHost));
    act(() => {
      useUi.setState({
        toasts: [
          { id: 1, kind: "success", text: "保存完成" },
          { id: 2, kind: "error", text: "连接失败" },
        ],
        appDialog: { kind: "message", message: "结果", level: "info", resolve: vi.fn() },
      });
    });

    const announcer = mounted.container.querySelector<HTMLElement>("[data-a11y-announcer]");
    expect(announcer?.querySelector('[role="alert"]')?.textContent).toBe("连接失败");
    expect(announcer?.querySelector('[role="status"]')?.textContent).toBe("保存完成");
    expect(announcer?.hasAttribute("inert")).toBe(false);
    expect(announcer?.getAttribute("aria-hidden")).toBeNull();
  });

  it("defers public dialogs and prompts instead of replacing unsettled requests", async () => {
    const firstDialog = deferred<boolean>();
    const secondDialog = deferred<boolean>();
    const askHandler = vi.fn((message: string) =>
      message === "first" ? firstDialog.promise : secondDialog.promise,
    );
    registerDialogHandlers({
      ask: askHandler,
      confirm: vi.fn(),
      message: vi.fn(),
      choose: vi.fn(),
    });

    const firstResult = ask("first");
    const secondResult = ask("second");
    expect(askHandler).toHaveBeenCalledOnce();
    firstDialog.resolve(true);
    await waitFor(() => expect(askHandler).toHaveBeenCalledTimes(2));
    secondDialog.resolve(false);
    await expect(firstResult).resolves.toBe(true);
    await expect(secondResult).resolves.toBe(false);

    const firstPrompt = deferred<string | null>();
    const secondPrompt = deferred<string | null>();
    const promptHandler = vi.fn((message: string) =>
      message === "one" ? firstPrompt.promise : secondPrompt.promise,
    );
    registerPromptHandler(promptHandler);
    const one = promptText("one");
    const two = promptText("two");
    expect(promptHandler).toHaveBeenCalledOnce();
    firstPrompt.resolve("1");
    await waitFor(() => expect(promptHandler).toHaveBeenCalledTimes(2));
    secondPrompt.resolve("2");
    await expect(one).resolves.toBe("1");
    await expect(two).resolves.toBe("2");
  });
});
