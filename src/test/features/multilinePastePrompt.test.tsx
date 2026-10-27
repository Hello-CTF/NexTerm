/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ ask: vi.fn() }));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { useUi, type AppTab, type Workspace } from "../../app/store";
import {
  getTerminalPrefs,
  registerAccountPreferenceStore,
  setTerminalConfirmMultilinePaste,
  type AccountPreferenceStore,
} from "../../app/preferences";
import { MultilinePasteDialogHost } from "../../features/terminal/MultilinePasteDialog";
import { resetMultilinePastePromptForTest } from "../../features/terminal/multilinePastePrompt";
import { confirmMultilinePaste } from "../../features/terminal/terminalPaste";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const KEY = "terminal.confirmMultilinePaste";
let host: MountedView | undefined;

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}

function seedTabs(ids: string[]): void {
  const workspace: Workspace = {
    id: "ws",
    kind: "session",
    title: "workspace",
    sessionId: "s",
    panes: [{ id: "p", tabs: ids.map(terminal), activeTabId: ids[0] ?? null }],
    activePaneId: "p",
    closable: true,
  };
  useUi.setState({ workspaces: [workspace], activeWorkspaceId: "ws" });
}

function memoryStore(overrides: Record<string, unknown> = {}) {
  const puts: Record<string, unknown>[] = [];
  const store: AccountPreferenceStore & { puts: Record<string, unknown>[] } = {
    puts,
    getView: async () => ({ defaults: {}, overrides: { ...overrides } }),
    putOverrides: async (set) => {
      puts.push(set);
    },
    deleteOverrides: async () => undefined,
  };
  return store;
}

function dialog(): HTMLElement | null {
  return document.querySelector('[role="alertdialog"]');
}

function button(text: string): HTMLButtonElement {
  const found = [...(dialog()?.querySelectorAll("button") ?? [])].find((node) => node.textContent?.trim() === text);
  if (!found) throw new Error(`button ${text} not found`);
  return found;
}

function checkbox(label: string): HTMLInputElement {
  const found = [...(dialog()?.querySelectorAll("label") ?? [])].find((node) => node.textContent?.includes(label));
  const input = found?.querySelector("input");
  if (!input) throw new Error(`checkbox ${label} not found`);
  return input;
}

function key(target: Element, keyName: string, repeat = false): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key: keyName, repeat, bubbles: true, cancelable: true });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function keyUp(target: Element, keyName: string): void {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keyup", { key: keyName, bubbles: true, cancelable: true }));
  });
}

// paste starts a paste and waits until the dialog shows it, or until the paste settled without one.
async function paste(text: string, tabId?: string): Promise<{ result: Promise<boolean>; prompted: boolean }> {
  let settled = false;
  let result: Promise<boolean> = Promise.resolve(false);
  act(() => {
    result = confirmMultilinePaste(text, tabId).finally(() => {
      settled = true;
    });
  });
  await flush();
  return { result, prompted: !settled && dialog() !== null };
}

beforeEach(() => {
  document.body.replaceChildren();
  mocks.ask.mockReset();
  registerAccountPreferenceStore(null);
  setTerminalConfirmMultilinePaste(true);
  localStorage.clear();
  resetMultilinePastePromptForTest();
  seedTabs(["t1", "t2"]);
  host = mount(<MultilinePasteDialogHost />);
});

afterEach(() => {
  host?.unmount();
  host = undefined;
  resetMultilinePastePromptForTest();
  registerAccountPreferenceStore(null);
});

describe("multi-line paste prompt", () => {
  it("never prompts for a single line", async () => {
    const { result, prompted } = await paste("ls -la\n", "t1");
    expect(prompted).toBe(false);
    await expect(result).resolves.toBe(true);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("previews the first lines with the warning", async () => {
    const { result, prompted } = await paste(["l1", "l2", "l3", "l4", "l5", "l6", "l7"].join("\n"), "t1");
    expect(prompted).toBe(true);
    const text = dialog()?.textContent ?? "";
    expect(text).toContain("即将向终端粘贴 7 行文本");
    expect(text).toContain("多行内容可能包含多条命令，部分程序会立即逐行执行，请确认来源和内容。");
    const preview = dialog()?.querySelector('[aria-label="粘贴内容预览"]')?.textContent ?? "";
    expect(preview).toContain("l5");
    expect(preview).not.toContain("l6");
    expect(preview).toContain("…（共 7 行）");
    expect(mocks.ask).not.toHaveBeenCalled();
    click(button("粘贴"));
    await expect(result).resolves.toBe(true);
    expect(dialog()).toBeNull();
  });

  it("puts the initial focus on cancel", async () => {
    const { result } = await paste("a\nb", "t1");
    expect(document.activeElement).toBe(button("取消"));
    click(button("取消"));
    await expect(result).resolves.toBe(false);
  });

  it("pastes nothing on an auto-repeated Enter on the focused button", async () => {
    const { result } = await paste("a\nb", "t1");
    keyUp(document.body, "Control");
    for (const target of [button("取消"), button("粘贴")]) {
      target.focus();
      expect(key(target, "Enter", true).defaultPrevented).toBe(true);
    }
    expect(dialog()).not.toBeNull();
    click(button("取消"));
    await expect(result).resolves.toBe(false);
  });

  it("ignores an Enter on paste until a key has been released in the dialog", async () => {
    const { result } = await paste("a\nb", "t1");
    const pasteButton = button("粘贴");
    pasteButton.focus();
    expect(key(pasteButton, "Enter").defaultPrevented).toBe(true);
    expect(dialog()).not.toBeNull();

    keyUp(pasteButton, "Enter");
    key(pasteButton, "Enter");
    await expect(result).resolves.toBe(true);
    expect(dialog()).toBeNull();
  });

  it("pastes on Enter and cancels on Escape", async () => {
    const first = await paste("a\nb", "t1");
    keyUp(document.body, "Control");
    key(checkbox("本终端不再提示"), "Enter");
    await expect(first.result).resolves.toBe(true);

    const second = await paste("a\nb", "t1");
    expect(second.prompted).toBe(true);
    key(document.activeElement ?? document.body, "Escape");
    await expect(second.result).resolves.toBe(false);
    expect(dialog()).toBeNull();
  });

  it("does not paste from the cancel button on Enter", async () => {
    const { result } = await paste("a\nb", "t1");
    const cancel = button("取消");
    cancel.focus();
    key(cancel, "Enter");
    click(cancel);
    await expect(result).resolves.toBe(false);
  });

  it("skips only the chosen tab, keeps the choice across a remount, and forgets it when the tab closes", async () => {
    const first = await paste("a\nb", "t1");
    act(() => {
      checkbox("本终端不再提示").click();
    });
    click(button("粘贴"));
    await expect(first.result).resolves.toBe(true);

    const again = await paste("c\nd", "t1");
    expect(again.prompted).toBe(false);
    await expect(again.result).resolves.toBe(true);

    const other = await paste("c\nd", "t2");
    expect(other.prompted).toBe(true);
    click(button("取消"));
    await expect(other.result).resolves.toBe(false);

    host?.unmount();
    host = mount(<MultilinePasteDialogHost />);
    const afterRemount = await paste("e\nf", "t1");
    expect(afterRemount.prompted).toBe(false);
    await expect(afterRemount.result).resolves.toBe(true);

    seedTabs(["t2"]);
    seedTabs(["t1", "t2"]);
    const reopened = await paste("g\nh", "t1");
    expect(reopened.prompted).toBe(true);
    click(button("取消"));
    await expect(reopened.result).resolves.toBe(false);
  });

  it("ignores the options when the paste is cancelled", async () => {
    const first = await paste("a\nb", "t1");
    act(() => {
      checkbox("本终端不再提示").click();
      checkbox("以后都不再提示").click();
    });
    click(button("取消"));
    await expect(first.result).resolves.toBe(false);
    expect(getTerminalPrefs().confirmMultilinePaste).toBe(true);
    expect((await paste("a\nb", "t1")).prompted).toBe(true);
  });

  it("offers the tab option only for a workspace tab", async () => {
    const { result } = await paste("a\nb");
    expect(dialog()?.textContent).not.toContain("本终端不再提示");
    expect(dialog()?.textContent).toContain("以后都不再提示");
    click(button("取消"));
    await expect(result).resolves.toBe(false);
  });

  it("turns the prompt off everywhere through the synced account preference", async () => {
    const store = memoryStore();
    registerAccountPreferenceStore(store);
    await flush();

    const first = await paste("a\nb", "t1");
    act(() => {
      checkbox("以后都不再提示").click();
    });
    click(button("粘贴"));
    await expect(first.result).resolves.toBe(true);
    expect(store.puts).toContainEqual({ [KEY]: false });
    expect(getTerminalPrefs().confirmMultilinePaste).toBe(false);

    const other = await paste("a\nb", "t2");
    expect(other.prompted).toBe(false);
    await expect(other.result).resolves.toBe(true);

    setTerminalConfirmMultilinePaste(true);
    expect(store.puts).toContainEqual({ [KEY]: true });
    const back = await paste("a\nb", "t2");
    expect(back.prompted).toBe(true);
    click(button("取消"));
    await expect(back.result).resolves.toBe(false);
  });

  it("honours the preference synced from another device", async () => {
    registerAccountPreferenceStore(memoryStore({ [KEY]: false }));
    await vi.waitFor(() => expect(getTerminalPrefs().confirmMultilinePaste).toBe(false));
    const { result, prompted } = await paste("a\nb", "t1");
    expect(prompted).toBe(false);
    await expect(result).resolves.toBe(true);
  });

  it("cancels an open prompt when the account changes", async () => {
    const { result } = await paste("a\nb", "t1");
    act(() => {
      useUi.setState((state) => ({ accountEpoch: state.accountEpoch + 1 }));
    });
    await expect(result).resolves.toBe(false);
    expect(dialog()).toBeNull();
  });
});
