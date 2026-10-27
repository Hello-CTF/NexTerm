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

import { ChoiceButtons, SessionPromptChoices, setChoiceClockForTest } from "../../features/terminal/ChoiceButtons";
import { TerminalPromptBar } from "../../features/terminal/TerminalPromptBar";
import { InboxButton } from "../../features/inbox/InboxButton";
import { StatusWall } from "../../features/mobile/StatusWall";
import { ANSWER_TAKES_SIZE_NOTE, CHOICE_ARM_MS, OPEN_TO_ANSWER_LABEL } from "../../features/terminal/promptChoices";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import { applyAggregateList, resetSessionAggregateForTest } from "../../app/sessionAggregate";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { click, deferred, flush, mount, waitFor, type MountedView } from "./reactTestUtils";
import type { AttentionEvent, AttentionPrompt } from "../../ipc/generated/types";

const NOW = Date.parse("2026-10-27T12:00:00Z");
let clockNow = 0;

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

function needsMe(tabId: string, p: AttentionPrompt | null, version = 1, minutesAgo = 1): AttentionEvent {
  return {
    tabId,
    state: "needs_me",
    confidence: "heuristic",
    source: "helper",
    reason: "prompt",
    since: new Date(NOW - minutesAgo * 60_000).toISOString(),
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

function buttons(root: ParentNode): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>("[data-prompt-option]")];
}

function statusText(view: MountedView): string {
  return view.container.querySelector('[data-testid="prompt-status"]')?.textContent ?? "";
}

// The buttons held still long enough to count a tap.
function settle(): void {
  clockNow += CHOICE_ARM_MS + 50;
}

function pointerDown(target: Element): Event {
  const event = new MouseEvent("pointerdown", { bubbles: true, cancelable: true, detail: 1 });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function pointerClick(target: Element): void {
  act(() => {
    target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 }));
  });
}

// A finger press: down, then the click it ends with.
function press(target: Element): Event {
  const down = pointerDown(target);
  pointerClick(target);
  act(() => {
    window.dispatchEvent(new Event("pointerup"));
  });
  return down;
}

function choices(tabId: string, p: AttentionPrompt, version = 1, extra: Record<string, unknown> = {}) {
  return createElement(ChoiceButtons, { tabId, prompt: p, version, ...extra });
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.spyOn(Date, "now").mockReturnValue(NOW);
  clockNow = 1000;
  setChoiceClockForTest(() => clockNow);
  resetAwarenessStore();
  resetSessionAggregateForTest();
  mocks.attentionList.mockResolvedValue([]);
  mocks.sessionsAggregate.mockResolvedValue({ hosts: [], sessions: [], complete: true });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [] });
});

afterEach(() => {
  vi.restoreAllMocks();
  setChoiceClockForTest(null);
  resetAwarenessStore();
  resetSessionAggregateForTest();
});

describe("choice buttons", () => {
  it("shows the question with the options and sends the tapped option with the prompt identity", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    const p = prompt();
    const view = mount(choices("tab-1", p));
    try {
      const question = view.container.querySelector('[data-testid="prompt-question"]');
      expect(question?.textContent).toBe(p.question);
      const group = view.container.querySelector('[role="group"]');
      expect(group?.getAttribute("aria-labelledby")).toBe(question?.id);
      expect(buttons(view.container).map((button) => button.textContent)).toEqual(["yes", "no（默认）"]);

      settle();
      press(buttons(view.container)[0]);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.tapPrompt).toHaveBeenCalledWith("tab-1", p, 0);
      expect(statusText(view)).toBe("已发送「yes」");
      // The tap consumed the prompt epoch: the same instance cannot be answered twice.
      expect(buttons(view.container).every((button) => button.disabled)).toBe(true);
    } finally {
      view.unmount();
    }
  });

  it("ignores a tap before the buttons held still, and one pressed before they were armed", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    const view = mount(choices("tab-1", prompt()));
    try {
      press(buttons(view.container)[0]);
      // A keyboard click right after the prompt appeared does not count either.
      click(buttons(view.container)[0]);
      // Pressed early, released after the arming delay.
      pointerDown(buttons(view.container)[0]);
      settle();
      pointerClick(buttons(view.container)[0]);
      await flush();
      expect(mocks.tapPrompt).not.toHaveBeenCalled();
      // A keyboard click once armed answers.
      click(buttons(view.container)[1]);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });

  it("sends nothing when a row is inserted above the prompt during a tap", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    // Rows are 100 px tall; a button's position follows its row's place in the list.
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      const row = this.closest("li");
      const index = row?.parentElement ? [...row.parentElement.children].indexOf(row) : 0;
      return { left: 0, top: index * 100, right: 200, bottom: index * 100 + 40, width: 200, height: 40, x: 0, y: index * 100, toJSON: () => ({}) } as DOMRect;
    });
    const list = (ids: string[]) =>
      createElement("ul", null, ids.map((id) => createElement("li", { key: id, "data-row": id }, choices(id, prompt({ fingerprint: id })))));
    const view = mount(list(["tab-a"]));
    try {
      settle();
      const yesA = view.container.querySelector<HTMLButtonElement>('[data-row="tab-a"] [data-prompt-option="0"]')!;
      pointerDown(yesA);
      act(() => view.root.render(list(["tab-b", "tab-a"])));
      settle();
      pointerClick(yesA);
      await flush();
      expect(mocks.tapPrompt).not.toHaveBeenCalled();

      // Once the list held still again, a new press answers the prompt under the finger.
      settle();
      press(view.container.querySelector('[data-row="tab-b"] [data-prompt-option="1"]')!);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.tapPrompt).toHaveBeenCalledWith("tab-b", expect.objectContaining({ fingerprint: "tab-b" }), 1);
    } finally {
      view.unmount();
    }
  });

  it("lets only one tap through while one is in flight", async () => {
    const pending = deferred<void>();
    mocks.tapPrompt.mockReturnValue(pending.promise);
    const view = mount(choices("tab-1", prompt()));
    try {
      settle();
      const [yes, no] = buttons(view.container);
      act(() => {
        yes.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        yes.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      click(no);
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(buttons(view.container).every((button) => button.disabled)).toBe(true);
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
      settle();
      press(buttons(view.container)[1]);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.attentionList).toHaveBeenCalledTimes(1);
      // The newer prompt instance replaced the refused one with fresh buttons; nothing was sent again.
      await waitFor(() => expect(buttons(view.container).every((button) => !button.disabled)).toBe(true));
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.write).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("keeps a refused prompt's buttons disabled with the notice until the prompt changes", async () => {
    mocks.tapPrompt.mockRejectedValue({ code: "stale_prompt", message: "prompt changed" });
    const view = mount(choices("tab-1", prompt()));
    try {
      settle();
      press(buttons(view.container)[0]);
      await flush();
      expect(statusText(view)).toBe("提示已变化");
      expect(buttons(view.container).every((button) => button.disabled)).toBe(true);
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });

  it("says a spectating device cannot answer", async () => {
    mocks.tapPrompt.mockRejectedValue({ code: "forbidden", message: "spectator" });
    const view = mount(choices("tab-1", prompt()));
    try {
      settle();
      press(buttons(view.container)[0]);
      await flush();
      expect(statusText(view)).toBe("此端只在旁观这个终端，不能回答");
      expect(buttons(view.container).every((button) => button.disabled)).toBe(true);
    } finally {
      view.unmount();
    }
  });

  it("shows a non-tappable prompt, or one without a helper generation, with a hint and no buttons", () => {
    const blocked = mount(choices("tab-1", prompt({ kind: "question", question: "Password:", options: [], tappable: false, blocked: "question" })));
    const unnamed = mount(choices("tab-2", prompt({ generation: undefined })));
    try {
      expect(blocked.container.querySelector('[data-testid="prompt-question"]')?.textContent).toBe("Password:");
      expect(blocked.container.querySelector('[data-testid="prompt-note"]')?.textContent).toContain("请在终端里回答");
      expect(blocked.container.querySelectorAll("button")).toHaveLength(0);
      expect(unnamed.container.querySelector('[data-testid="prompt-note"]')?.textContent).toContain("请在终端里回答");
      expect(unnamed.container.querySelectorAll("button")).toHaveLength(0);
    } finally {
      blocked.unmount();
      unnamed.unmount();
    }
  });

  it("keeps the terminal's focus and IME composition when a button is pressed", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    const textarea = document.createElement("textarea");
    document.body.append(textarea);
    const view = mount(choices("tab-1", prompt()));
    try {
      textarea.focus();
      textarea.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
      settle();
      const down = press(buttons(view.container)[0]);
      await flush();
      expect(down.defaultPrevented).toBe(true);
      expect(document.activeElement).toBe(textarea);
      // The answer goes through the tap, never as typed input into the composition.
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(mocks.write).not.toHaveBeenCalled();
    } finally {
      view.unmount();
      textarea.remove();
    }
  });
});

describe("separate Enter", () => {
  const separate = prompt({ submit: "separate", enterKey: "\r", options: [{ label: "y", keys: "y" }, { label: "n", keys: "n" }] });

  it("is offered only after this view's tap, once, and sends the prompt's Enter", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    mocks.write.mockResolvedValue(undefined);
    const view = mount(choices("tab-1", separate, 3, { allowEnter: true }));
    try {
      const enter = () => view.container.querySelector<HTMLButtonElement>("[data-prompt-enter]")!;
      expect(enter().getAttribute("aria-label")).toBe("发送回车");
      expect(enter().disabled).toBe(true);
      settle();
      press(buttons(view.container)[0]);
      await flush();
      expect(enter().disabled).toBe(false);
      press(enter());
      await flush();
      expect(mocks.write).toHaveBeenCalledTimes(1);
      const [tabId, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
      expect(tabId).toBe("tab-1");
      expect(new TextDecoder().decode(bytes)).toBe("\r");
      expect(enter().disabled).toBe(true);
      press(enter());
      await flush();
      expect(mocks.write).toHaveBeenCalledTimes(1);
    } finally {
      view.unmount();
    }
  });

  it("is withdrawn once a newer state of the tab arrives", async () => {
    mocks.tapPrompt.mockResolvedValue(undefined);
    const view = mount(choices("tab-1", separate, 3, { allowEnter: true }));
    try {
      settle();
      press(buttons(view.container)[0]);
      await flush();
      act(() => view.root.render(choices("tab-1", separate, 4, { allowEnter: true })));
      const enter = view.container.querySelector<HTMLButtonElement>("[data-prompt-enter]")!;
      expect(enter.disabled).toBe(true);
      press(enter);
      await flush();
      expect(mocks.write).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("is not offered outside the terminal pane", () => {
    applyAwareness([needsMe("tab-1", separate)]);
    const awareness = { state: "needs_me" as const, inferred: true, seen: false, notable: false, version: 1, prompt: separate };
    const listEntry = mount(createElement(SessionPromptChoices, { tabId: "tab-1", awareness, attachedHere: true }));
    const pane = mount(createElement(TerminalPromptBar, { tabId: "tab-1" }));
    try {
      expect(listEntry.container.querySelector("[data-prompt-enter]")).toBeNull();
      expect(pane.container.querySelector("[data-prompt-enter]")).not.toBeNull();
    } finally {
      listEntry.unmount();
      pane.unmount();
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

  it("answers from an inbox entry of a tab here, and opens a listed-only session to answer it", async () => {
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
      const here = view.container.querySelector('[data-inbox-tab="tab-here"] [data-inbox-actions]')!;
      const far = view.container.querySelector('[data-inbox-tab="tab-far"] [data-inbox-actions]')!;
      expect(buttons(far)).toHaveLength(0);
      expect(far.querySelector("[data-prompt-open]")?.textContent).toBe(OPEN_TO_ANSWER_LABEL);
      expect(far.textContent).toContain("Are you sure you want to continue connecting");
      expect(here.textContent).toContain(ANSWER_TAKES_SIZE_NOTE);
      settle();
      press(here.querySelector('[data-prompt-option="0"]')!);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledWith("tab-here", expect.objectContaining({ fingerprint: "fp-1" }), 0);
    } finally {
      view.unmount();
    }
  });

  it("opens a listed-only session through the given action", () => {
    const onOpen = vi.fn();
    const awareness = { state: "needs_me" as const, inferred: true, seen: false, notable: false, version: 1, prompt: prompt() };
    const view = mount(createElement(SessionPromptChoices, { tabId: "tab-far", awareness, attachedHere: false, onOpen }));
    const unreachable = mount(createElement(SessionPromptChoices, { tabId: "tab-far", awareness, attachedHere: false }));
    try {
      click(view.container.querySelector("[data-prompt-open]")!);
      expect(onOpen).toHaveBeenCalledTimes(1);
      expect(mocks.tapPrompt).not.toHaveBeenCalled();
      expect(unreachable.container.querySelectorAll("button")).toHaveLength(0);
      expect(unreachable.container.textContent).toContain("此端无法打开这个终端");
    } finally {
      view.unmount();
      unreachable.unmount();
    }
  });

  it("keeps the inbox order while it is open and holds the rows while a pointer is down", () => {
    useUi.setState({ workspaces: [workspace([terminal("t1", "tab-old"), terminal("t2", "tab-new"), terminal("t3", "tab-newest")])], activeWorkspaceId: "ws" });
    applyAwareness([needsMe("tab-old", prompt({ fingerprint: "old" }), 1, 10)]);
    const view = mount(createElement(InboxButton));
    const rows = () => [...view.container.querySelectorAll<HTMLElement>("[data-inbox-tab]")].map((row) => row.dataset.inboxTab);
    try {
      click(view.container.querySelector("button[aria-haspopup]")!);
      // A newer state would sort first; while the list is open it joins at the end.
      act(() => applyAwareness([needsMe("tab-new", prompt({ fingerprint: "new" }), 1, 1)]));
      expect(rows()).toEqual(["tab-old", "tab-new"]);
      // While a finger is down no row comes or goes.
      pointerDown(view.container.querySelector('[data-inbox-tab="tab-old"]')!);
      act(() => applyAwareness([needsMe("tab-newest", prompt({ fingerprint: "newest" }), 1, 0)]));
      expect(rows()).toEqual(["tab-old", "tab-new"]);
      act(() => {
        window.dispatchEvent(new Event("pointerup"));
      });
      expect(rows()).toEqual(["tab-old", "tab-new", "tab-newest"]);
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
      cardActions: (card, controls) => createElement(SessionPromptChoices, {
        tabId: card.tabId,
        awareness: card.awareness,
        attachedHere: card.uiTabId !== undefined,
        onOpen: controls.open,
        opening: controls.opening,
      }),
    }));
    try {
      const actions = view.container.querySelector('[data-wall-card="tab-here"] [data-wall-actions]')!;
      expect(actions.textContent).toContain("Are you sure you want to continue connecting");
      settle();
      press(actions.querySelector('[data-prompt-option="0"]')!);
      await flush();
      expect(mocks.tapPrompt).toHaveBeenCalledTimes(1);
      expect(onOpened).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("holds the wall's cards still while a pointer is down", () => {
    useUi.setState({ workspaces: [workspace([terminal("t1", "tab-a"), terminal("t2", "tab-b")])], activeWorkspaceId: "ws" });
    applyAwareness([needsMe("tab-a", prompt({ fingerprint: "a" }))]);
    const view = mount(createElement(StatusWall, { onOpened: vi.fn() }));
    const needsMeCards = () => [...view.container.querySelectorAll<HTMLElement>('[data-wall-group="needs_me"] [data-wall-card]')].map((card) => card.dataset.wallCard);
    try {
      expect(needsMeCards()).toEqual(["tab-a"]);
      pointerDown(view.container.querySelector('[data-wall-card="tab-a"]')!);
      act(() => applyAwareness([needsMe("tab-b", prompt({ fingerprint: "b" }))]));
      expect(needsMeCards()).toEqual(["tab-a"]);
      act(() => {
        window.dispatchEvent(new Event("pointerup"));
      });
      expect(needsMeCards()).toEqual(["tab-a", "tab-b"]);
    } finally {
      view.unmount();
    }
  });
});
