/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  tapPrompt: vi.fn(),
  write: vi.fn(),
  attentionList: vi.fn(),
  sessionsAggregate: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: {
      tapPrompt: mocks.tapPrompt,
      write: mocks.write,
      attentionList: mocks.attentionList,
      sessionsAggregate: mocks.sessionsAggregate,
    },
  });
});

import { ChoiceButtons, SessionPromptChoices } from "../../features/terminal/ChoiceButtons";
import { TerminalPromptBar } from "../../features/terminal/TerminalPromptBar";
import { InboxButton } from "../../features/inbox/InboxButton";
import { StatusWall } from "../../features/mobile/StatusWall";
import { OPEN_TO_ANSWER } from "../../features/terminal/promptChoices";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import { applyAggregateList, resetSessionAggregateForTest } from "../../app/sessionAggregate";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { click, deferred, flush, mount, waitFor, type MountedView } from "./reactTestUtils";
import type { AttentionEvent, AttentionPrompt } from "../../ipc/generated/types";

const NOW = Date.parse("2026-10-27T12:00:00Z");

function prompt(patch: Partial<AttentionPrompt> = {}): AttentionPrompt {
  return {
    kind: "yesno",
    question: "Are you sure you want to continue connecting (yes/no/[fingerprint])?",
    options: [{ label: "yes", keys: "yes\r" }, { label: "no", keys: "no\r", default: true }],
    cursorIndex: 0,
    fingerprint: "fp-1",
    epoch: 4,
    generation: "gen-1",
    tappable: true,
    method: "keys",
    submit: "enter",
    ...patch,
  };
}

function needsMe(tabId: string, p: AttentionPrompt | null, version = 1): AttentionEvent {
  return {
    tabId,
    state: "needs_me",
    confidence: "heuristic",
    source: "helper",
    reason: "prompt",
    since: new Date(NOW - 60_000).toISOString(),
    version,
    ...(p ? { prompt: p } : {}),
  };
}

function terminal(id: string, tabId: string): AppTab {
  return { id, kind: "terminal", title: `标签 ${id}`, sessionId: "s1", tabId, closable: true };
}

function workspace(tabs: AppTab[]): Workspace {
  return { id: "ws", kind: "session", title: "web-01", sessionId: "s1", closable: true, activePaneId: "p", panes: [{ id: "p", tabs, activeTabId: tabs[0]?.id ?? null }] };
}

function buttons(view: MountedView): HTMLButtonElement[] {
  return [...view.container.querySelectorAll<HTMLButtonElement>("[data-prompt-option]")];
}

function statusText(view: MountedView): string {
  return view.container.querySelector('[data-testid="prompt-status"]')?.textContent ?? "";
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.spyOn(Date, "now").mockReturnValue(NOW);
  resetAwarenessStore();
  resetSessionAggregateForTest();
  mocks.attentionList.mockResolvedValue([]);
  mocks.sessionsAggregate.mockResolvedValue({ hosts: [], sessions: [], complete: true });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [] });
});

afterEach(() => {
  vi.restoreAllMocks();
  resetAwarenessStore();
  resetSessionAggregateForTest();
});

describe("choice buttons", () => {
  it("shows the question with the options and sends the tapped option with the prompt identity", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    const p = prompt();
    const view = mount(createElement(ChoiceButtons, { tabId: "tab-1", prompt: p }));
    try {
      const question = view.container.querySelector('[data-testid="prompt-question"]');
      expect(question?.textContent).toBe(p.question);
      const group = view.container.querySelector('[role="group"]');
      expect(group?.getAttribute("aria-labelledby")).toBe(question?.id);
      expect(buttons(view).map((button) => button.textContent)).toEqual(["yes", "no（默认）"]);

      click(buttons(view)[0]);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.tapPrompt).toHaveBeenCalledWith("tab-1", p, 0);
      expect(statusText(view)).toBe("已发送「yes」");
      // The tap consumed the prompt epoch: the same instance cannot be answered twice.
      expect(buttons(view).every((button) => button.disabled)).toBe(true);
    } finally {
      view.unmount();
    }
  });

  it("lets only one tap through while one is in flight", async () => {
    const pending = deferred<void>();
    mocks.tapPrompt.mockReturnValue(pending.promise);
    const view = mount(createElement(ChoiceButtons, { tabId: "tab-1", prompt: prompt() }));
    try {
      const [yes, no] = buttons(view);
      act(() => {
        yes.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        yes.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      click(no);
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(buttons(view).every((button) => button.disabled)).toBe(true);
      expect(view.container.querySelector('[role="group"]')?.getAttribute("aria-busy")).toBe("true");
      pending.resolve();
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });

  it("says the prompt changed on stale_prompt, reloads the states and does not retry", async () => {
    mocks.tapPrompt.mockRejectedValue({ code: "stale_prompt", message: "prompt changed" });
    const newer = prompt({ epoch: 5, fingerprint: "fp-2" });
    mocks.attentionList.mockResolvedValue([needsMe("tab-1", newer, 2)]);
    applyAwareness([needsMe("tab-1", prompt())]);
    const view = mount(createElement(TerminalPromptBar, { tabId: "tab-1" }));
    try {
      click(buttons(view)[1]);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.attentionList).toHaveBeenCalledTimes(1);
      // The newer prompt instance replaced the refused one with fresh buttons; nothing was sent again.
      await waitFor(() => expect(buttons(view).every((button) => !button.disabled)).toBe(true));
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.write).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("keeps a refused prompt's buttons disabled with the notice until the prompt changes", async () => {
    mocks.tapPrompt.mockRejectedValue({ code: "stale_prompt", message: "prompt changed" });
    const view = mount(createElement(ChoiceButtons, { tabId: "tab-1", prompt: prompt() }));
    try {
      click(buttons(view)[0]);
      await flush();
      expect(statusText(view)).toBe("提示已变化");
      expect(buttons(view).every((button) => button.disabled)).toBe(true);
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });

  it("shows a non-tappable prompt's question with a hint and no buttons", () => {
    const view = mount(createElement(ChoiceButtons, {
      tabId: "tab-1",
      prompt: prompt({ kind: "question", question: "Password:", options: [], tappable: false, blocked: "question" }),
    }));
    try {
      expect(view.container.querySelector('[data-testid="prompt-question"]')?.textContent).toBe("Password:");
      expect(view.container.querySelector('[data-testid="prompt-note"]')?.textContent).toContain("请在终端里回答");
      expect(view.container.querySelectorAll("button")).toHaveLength(0);
    } finally {
      view.unmount();
    }
  });

  it("offers Enter as its own action when the prompt asks for it", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    mocks.write.mockResolvedValue(undefined);
    const view = mount(createElement(ChoiceButtons, {
      tabId: "tab-1",
      prompt: prompt({ submit: "separate", enterKey: "\r", options: [{ label: "y", keys: "y" }, { label: "n", keys: "n" }] }),
    }));
    try {
      click(buttons(view)[0]);
      await flush();
      const enter = view.container.querySelector<HTMLButtonElement>("[data-prompt-enter]");
      expect(enter?.getAttribute("aria-label")).toBe("发送回车");
      expect(enter?.disabled).toBe(false);
      click(enter!);
      await flush();
      expect(mocks.write).toHaveBeenCalledTimes(1);
      const [tabId, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
      expect(tabId).toBe("tab-1");
      expect(new TextDecoder().decode(bytes)).toBe("\r");
    } finally {
      view.unmount();
    }
  });
});

describe("prompt choices in lists and the pane", () => {
  it("shows the pane bar only while the session needs the user with a prompt, also after it was seen", () => {
    applyAwareness([needsMe("tab-1", prompt())]);
    const view = mount(createElement(TerminalPromptBar, { tabId: "tab-1" }));
    try {
      expect(view.container.querySelector("[data-terminal-prompt-bar]")).not.toBeNull();
      act(() => applyAwareness([{ ...needsMe("tab-1", prompt()), seen: true, version: 2 }]));
      expect(view.container.querySelector("[data-terminal-prompt-bar]")).not.toBeNull();
      act(() => applyAwareness([{ ...needsMe("tab-1", null), state: "running", version: 3 }]));
      expect(view.container.querySelector("[data-terminal-prompt-bar]")).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("answers from an inbox entry of a tab here, and asks to open a listed-only session first", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    useUi.setState({ workspaces: [workspace([terminal("t1", "tab-here")])], activeWorkspaceId: "ws" });
    applyAwareness([needsMe("tab-here", prompt())]);
    applyAggregateList({
      hosts: [{ key: "device:box", kind: "device", name: "build box", status: "ok", fetchedAt: NOW }],
      sessions: [{ hostKey: "device:box", tabId: "tab-far", cols: 80, rows: 24, attention: needsMe("tab-far", prompt({ fingerprint: "fp-far" })) }],
      complete: true,
    });
    const view = mount(createElement(InboxButton));
    try {
      click(view.container.querySelector("button[aria-haspopup]")!);
      const here = view.container.querySelector('[data-inbox-tab="tab-here"] [data-inbox-actions]');
      const far = view.container.querySelector('[data-inbox-tab="tab-far"] [data-inbox-actions]');
      expect(far?.textContent).toContain(OPEN_TO_ANSWER);
      expect([...far!.querySelectorAll<HTMLButtonElement>("[data-prompt-option]")].every((button) => button.disabled)).toBe(true);
      const yes = here!.querySelector<HTMLButtonElement>('[data-prompt-option="0"]')!;
      expect(yes.disabled).toBe(false);
      click(yes);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledWith("tab-here", expect.objectContaining({ fingerprint: "fp-1" }), 0);
    } finally {
      view.unmount();
    }
  });

  it("puts the question and buttons on the status wall card without opening the session", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    useUi.setState({ workspaces: [workspace([terminal("t1", "tab-here")])], activeWorkspaceId: "ws" });
    applyAwareness([needsMe("tab-here", prompt())]);
    const onOpened = vi.fn();
    const view = mount(createElement(StatusWall, {
      onOpened,
      cardActions: (card) => createElement(SessionPromptChoices, { tabId: card.tabId, awareness: card.awareness, attachedHere: card.uiTabId !== undefined }),
    }));
    try {
      const actions = view.container.querySelector('[data-wall-card="tab-here"] [data-wall-actions]');
      expect(actions?.textContent).toContain("Are you sure you want to continue connecting");
      click(actions!.querySelector<HTMLButtonElement>('[data-prompt-option="0"]')!);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(onOpened).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });
});
