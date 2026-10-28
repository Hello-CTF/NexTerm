/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  attentionSeen: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, terminalApi: { ...actual.terminalApi, attentionSeen: mocks.attentionSeen } };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: () => Promise.resolve(() => {}) };
});

import { useUi, type AppTab, type Workspace } from "../../app/store";
import { applyAwareness, resetAwarenessStore, useAwarenessStore } from "../../app/sessionAwarenessStore";
import type { TerminalAttentionEvent } from "../../ipc/events";
import { buildInbox } from "../../features/inbox/inbox";
import { InboxButton } from "../../features/inbox/InboxButton";
import {
  NOTIFY_FALLBACK_MS,
  NOTIFY_GRACE_MS,
  considerAttention,
  refreshNotificationStatus,
  startInboxNotifications,
  stopInboxNotificationsForTest,
} from "../../features/inbox/inboxNotifications";
import { dismissInboxGuidance, resetInboxGuidanceForTest } from "../../features/inbox/firstRunGuidance";
import { resetNotificationPrefsForTest, setNotificationPrefs } from "../../features/inbox/notificationPrefs";
import { createBrowserNotifier, type SystemNotificationInput, type SystemNotifier } from "../../features/inbox/systemNotifications";
import type { NotificationClickEvent } from "../../ipc/events";

function terminal(id: string, tabId: string, extra: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s1", tabId, closable: true, ...extra };
}

function workspace(id: string, title: string, tabs: AppTab[]): Workspace {
  return { id, kind: "session", title, panes: [{ id: `${id}-p`, tabs, activeTabId: tabs[0]?.id ?? null }], activePaneId: `${id}-p`, closable: true };
}

// Two workspaces: web-01 is on screen with its build tab, db-01 holds a migration tab nobody looks at.
function seed(): void {
  useUi.setState({
    workspaces: [workspace("ws1", "web-01", [terminal("build", "T1")]), workspace("ws2", "db-01", [terminal("migrate", "T2")])],
    activeWorkspaceId: "ws1",
  });
}

let version = 0;

function attention(patch: Partial<TerminalAttentionEvent> & { tabId: string }): TerminalAttentionEvent {
  version += 1;
  return { state: "needs_me", confidence: "signal", source: "helper", reason: "bell", since: "2026-10-27T10:00:00Z", version, ...patch };
}

// live delivers an event the way the app does: the awareness store applies it and the notifier considers it.
function live(event: TerminalAttentionEvent): void {
  applyAwareness([event]);
  considerAttention(event);
}

class FakeNotifier implements SystemNotifier {
  shown: SystemNotificationInput[] = [];
  removed: string[] = [];
  click: (click: NotificationClickEvent) => void = () => {};
  status: SystemNotifier["status"] = () => Promise.resolve({ available: true, permission: "granted" });
  request: SystemNotifier["request"] = () => this.status();
  show = (notification: SystemNotificationInput) => {
    this.shown.push(notification);
    return Promise.resolve();
  };
  remove = (id: string) => {
    this.removed.push(id);
  };
  onClick = (handler: (click: NotificationClickEvent) => void) => {
    this.click = handler;
  };
}

let notifier: FakeNotifier;
let focused = true;
let toasts: string[] = [];
let toastActions: (() => void)[] = [];

// grace lets the grace period pass and the notification settle.
async function grace(): Promise<void> {
  await vi.advanceTimersByTimeAsync(NOTIFY_GRACE_MS);
}

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  resetNotificationPrefsForTest();
  // Keep the first-run guidance card out of the way; these tests exercise the list itself.
  resetInboxGuidanceForTest();
  dismissInboxGuidance();
  resetAwarenessStore();
  seed();
  focused = true;
  toasts = [];
  toastActions = [];
  notifier = new FakeNotifier();
  startInboxNotifications({
    notifier,
    appFocused: () => focused,
    clientId: () => "me",
    visibleTabIds: () => new Set(["T1"]),
    onError: () => {},
    toast: (text, action) => {
      toasts.push(text);
      toastActions.push(action.onSelect);
    },
  });
  mocks.attentionSeen.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
  stopInboxNotificationsForTest();
  resetAwarenessStore();
  vi.useRealTimers();
});

describe("inbox entries", () => {
  it("lists unseen sessions across workspaces, needs_me first and newest first", () => {
    applyAwareness([
      attention({ tabId: "T1", state: "just_finished", reason: "command_end", exitCode: 2, since: "2026-10-27T10:05:00Z" }),
      attention({ tabId: "T2", reason: "notify", title: "Deploy", body: "approve?", since: "2026-10-27T10:01:00Z" }),
      attention({ tabId: "T9", reason: "bell", since: "2026-10-27T10:03:00Z" }),
      attention({ tabId: "T3", seen: true }),
      attention({ tabId: "T4", state: "running", reason: "output" }),
    ]);
    const entries = buildInbox(useAwarenessStore.getState().byTabId, useUi.getState().workspaces);
    expect(entries.map((entry) => entry.tabId)).toEqual(["T9", "T2", "T1"]);
    const [unopened, deploy, build] = entries;
    expect(deploy).toMatchObject({ uiTabId: "migrate", title: "migrate", place: "db-01", detail: "Deploy：approve?" });
    expect(build).toMatchObject({ uiTabId: "build", detail: "命令已结束，退出码 2" });
    expect(build.badge.kind).toBe("just_finished");
    // A session no workspace here shows stays visible, but cannot be opened from this client.
    expect(unopened.uiTabId).toBeUndefined();
    expect(unopened.badge.kind).toBe("needs_me");
  });
});

describe("system notifications", () => {
  it("notifies a session in another workspace after the grace period and opens it on click", async () => {
    live(attention({ tabId: "T2", reason: "notify", body: "migration waits" }));
    expect(notifier.shown).toEqual([]);
    await grace();
    // The title is the workspace the user named; the program's text only reaches the body.
    expect(notifier.shown).toEqual([
      { id: "attention:T2", title: "db-01 · 需要处理", body: "来自 db-01：migration waits", tabId: "T2" },
    ]);
    expect(toasts).toEqual([]);

    notifier.click({ id: "attention:T2", tabId: "T2" });
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
  });

  it("does not notify a state another device saw within the grace period, and takes back one seen later", async () => {
    live(attention({ tabId: "T2" }));
    live(attention({ tabId: "T2", seen: true }));
    await grace();
    expect(notifier.shown).toEqual([]);

    live(attention({ tabId: "T2", since: "2026-10-27T11:00:00Z" }));
    await grace();
    expect(notifier.shown).toHaveLength(1);
    live(attention({ tabId: "T2", since: "2026-10-27T11:00:00Z", seen: true }));
    expect(notifier.removed).toEqual(["attention:T2"]);
  });

  it("merges repeated bells of one state and notifies again for new notification text", async () => {
    live(attention({ tabId: "T2" }));
    live(attention({ tabId: "T2" }));
    await grace();
    live(attention({ tabId: "T2" }));
    await grace();
    expect(notifier.shown).toHaveLength(1);

    live(attention({ tabId: "T2", reason: "notify", body: "second step" }));
    await grace();
    expect(notifier.shown.map((n) => n.id)).toEqual(["attention:T2", "attention:T2"]);
    expect(notifier.shown[1].body).toContain("second step");
  });

  it("stays quiet for a session on screen while the app has focus, but not once the app is in the background", async () => {
    live(attention({ tabId: "T1" }));
    await grace();
    expect(notifier.shown).toEqual([]);
    expect(toasts).toEqual([]);

    focused = false;
    live(attention({ tabId: "T1", since: "2026-10-27T11:00:00Z" }));
    await grace();
    expect(notifier.shown.map((n) => n.tabId)).toEqual(["T1"]);
  });

  it("notifies only long finished commands, needs_me by default, and follows the device preferences", async () => {
    live(attention({ tabId: "T2", state: "just_finished", reason: "command_end", exitCode: 0 }));
    await grace();
    expect(notifier.shown).toEqual([]);
    live(attention({ tabId: "T2", state: "just_finished", reason: "command_end", exitCode: 0, notable: true, since: "2026-10-27T11:00:00Z" }));
    await grace();
    expect(notifier.shown.map((n) => n.title)).toEqual(["db-01 · 命令已结束"]);

    setNotificationPrefs({ needsMe: false });
    live(attention({ tabId: "T2", since: "2026-10-27T12:00:00Z" }));
    await grace();
    expect(notifier.shown).toHaveLength(1);
    expect(toasts).toEqual([]);
  });

  it("falls back to an in-app toast while system notifications are off or unavailable", async () => {
    setNotificationPrefs({ system: false });
    live(attention({ tabId: "T2", reason: "notify", body: "migration waits" }));
    await grace();
    expect(notifier.shown).toEqual([]);
    expect(toasts).toEqual(["db-01 · 需要处理：migration waits"]);
    toastActions[0]();
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");

    setNotificationPrefs({ system: true });
    useUi.setState({ activeWorkspaceId: "ws1" });
    refreshNotificationStatus();
    notifier.status = () => Promise.resolve({ available: false, reason: "unsupported" });
    live(attention({ tabId: "T2", since: "2026-10-27T11:00:00Z" }));
    await grace();
    expect(notifier.shown).toEqual([]);
    expect(toasts).toHaveLength(2);
  });

  it("falls back to a toast when showing fails, and reads the status again for the next state", async () => {
    notifier.show = () => Promise.reject(new Error("permission refused"));
    live(attention({ tabId: "T2", reason: "notify", body: "migration waits" }));
    await grace();
    expect(toasts).toEqual(["db-01 · 需要处理：migration waits"]);

    notifier.status = () => Promise.resolve({ available: true, permission: "denied" });
    live(attention({ tabId: "T2", since: "2026-10-27T11:00:00Z", body: "second" }));
    await grace();
    expect(toasts).toEqual(["db-01 · 需要处理：migration waits", "db-01 · 需要处理：second"]);
  });

  it("notifies at the grace period when this device typed last", async () => {
    live(attention({ tabId: "T2", reason: "notify", body: "approve?", lastInputClient: "me" }));
    await grace();
    expect(notifier.shown).toHaveLength(1);
  });

  it("makes another device wait, then notifies only if the state is still unseen", async () => {
    live(attention({ tabId: "T2", reason: "notify", body: "approve?", lastInputClient: "other" }));
    await grace();
    expect(notifier.shown).toEqual([]);
    await vi.advanceTimersByTimeAsync(NOTIFY_FALLBACK_MS - NOTIFY_GRACE_MS);
    expect(notifier.shown).toHaveLength(1);
  });

  it("makes another device skip a state that was seen in the meantime", async () => {
    const event = attention({ tabId: "T2", reason: "notify", body: "approve?", lastInputClient: "other" });
    live(event);
    await vi.advanceTimersByTimeAsync(NOTIFY_FALLBACK_MS / 2);
    live({ ...event, seen: true, version: event.version + 1 });
    await vi.advanceTimersByTimeAsync(NOTIFY_FALLBACK_MS);
    expect(notifier.shown).toEqual([]);
    expect(toasts).toEqual([]);
  });

  it("does not replay states that were waiting when the list loaded", async () => {
    applyAwareness([attention({ tabId: "T2" })]);
    await vi.advanceTimersByTimeAsync(NOTIFY_GRACE_MS * 2);
    expect(notifier.shown).toEqual([]);
    expect(toasts).toEqual([]);
  });
});

describe("inbox button", () => {
  let view: MountedView | undefined;

  afterEach(() => {
    view?.unmount();
    view = undefined;
  });

  it("marks every listed session read from one button, shown only for two or more", async () => {
    vi.useRealTimers();
    applyAwareness([attention({ tabId: "T1" })]);
    view = mount(createElement(InboxButton));
    const bell = view.container.querySelector('button[aria-haspopup="dialog"]') as HTMLButtonElement;
    click(bell);
    expect([...view.container.querySelectorAll("button")].some((b) => b.textContent === "全部标记已读")).toBe(false);
    act(() => applyAwareness([attention({ tabId: "T2", since: "2026-10-27T10:05:00Z" })]));
    const all = [...view.container.querySelectorAll("button")].find((b) => b.textContent === "全部标记已读") as HTMLButtonElement;
    expect(all).toBeTruthy();
    click(all);
    await flush();
    expect(mocks.attentionSeen).toHaveBeenCalledWith("T1", "2026-10-27T10:00:00Z");
    expect(mocks.attentionSeen).toHaveBeenCalledWith("T2", "2026-10-27T10:05:00Z");
  });

  it("counts the sessions, jumps to one and marks one read on every device", async () => {
    vi.useRealTimers();
    applyAwareness([
      attention({ tabId: "T2", reason: "notify", body: "approve?" }),
      attention({ tabId: "T1", state: "just_finished", reason: "command_end", exitCode: 0 }),
    ]);
    view = mount(createElement(InboxButton));
    const bell = view.container.querySelector('button[aria-haspopup="dialog"]') as HTMLButtonElement;
    expect(bell.getAttribute("aria-label")).toContain("2 个会话");

    click(bell);
    // Focus moves into the list and comes back to the bell on Escape.
    expect(view.container.querySelector('[role="dialog"]')?.contains(document.activeElement)).toBe(true);
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(view.container.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(bell);

    click(bell);
    const rows = [...view.container.querySelectorAll("[data-inbox-tab]")];
    expect(rows.map((row) => row.getAttribute("data-inbox-tab"))).toEqual(["T2", "T1"]);
    expect(rows[0].querySelector('[data-attention="needs_me"]')).not.toBeNull();

    click(rows[1].querySelector('button[aria-label^="标记"]') as Element);
    await flush();
    expect(mocks.attentionSeen).toHaveBeenCalledWith("T1", "2026-10-27T10:00:00Z");

    click(rows[0].querySelector("button") as Element);
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
    expect(view.container.querySelector('[role="dialog"]')).toBeNull();

    act(() => applyAwareness([attention({ tabId: "T2", seen: true }), attention({ tabId: "T1", state: "idle", reason: "input" })]));
    expect(bell.getAttribute("aria-label")).toContain("暂无");

    // An older helper cannot share the mark: the entry clears on this device only.
    act(() => applyAwareness([attention({ tabId: "T2", since: "2026-10-27T11:00:00Z" })]));
    mocks.attentionSeen.mockRejectedValueOnce({ code: "unsupported", message: "old helper", detail: { reason: "seen_unsupported" } });
    click(view.container.querySelector('button[aria-haspopup="dialog"]') as Element);
    click(view.container.querySelector('[data-inbox-tab="T2"] button[aria-label^="标记"]') as Element);
    await flush();
    expect(bell.getAttribute("aria-label")).toContain("暂无");
  });

  it("explains disabled rows inline, starts focus on the first row, and closes when focus leaves", () => {
    vi.useRealTimers();
    applyAwareness([attention({ tabId: "T2", reason: "notify", body: "approve?" }), attention({ tabId: "GONE" })]);
    view = mount(createElement(InboxButton));
    const bell = view.container.querySelector('button[aria-haspopup="dialog"]') as HTMLButtonElement;
    click(bell);
    const first = view.container.querySelector("[data-inbox-tab] button");
    expect(document.activeElement).toBe(first);
    const gone = view.container.querySelector('[data-inbox-tab="GONE"]')!;
    expect(gone.querySelector("[data-inbox-reason]")?.textContent).toContain("当前客户端未打开该终端");
    expect(view.container.querySelector('[data-inbox-tab="T2"] [data-inbox-reason]')).toBeNull();

    // Focus moving to a terminal elsewhere closes the panel without taking the focus back.
    const outside = document.createElement("textarea");
    document.body.append(outside);
    act(() => outside.focus());
    expect(view.container.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(outside);
    outside.remove();
  });
});

describe("browser notifications", () => {
  it("maps the permission, replaces a notification by id and hands back clicks", async () => {
    const created: FakeNotification[] = [];
    class FakeNotification {
      static permission: NotificationPermission = "default";
      static requestPermission = vi.fn(() => {
        FakeNotification.permission = "granted";
        return Promise.resolve(FakeNotification.permission);
      });
      onclick: (() => void) | null = null;
      onclose: (() => void) | null = null;
      closed = false;
      constructor(
        readonly title: string,
        readonly options?: NotificationOptions,
      ) {
        created.push(this);
      }
      close() {
        this.closed = true;
      }
    }
    const browser = createBrowserNotifier(FakeNotification as unknown as typeof Notification);
    expect(await browser.status()).toMatchObject({ available: true, permission: "prompt" });
    expect(await browser.request()).toMatchObject({ permission: "granted" });

    const clicks: NotificationClickEvent[] = [];
    browser.onClick((event) => clicks.push(event));
    await browser.show({ id: "attention:T2", title: "first", tabId: "T2" });
    await browser.show({ id: "attention:T2", title: "second", tabId: "T2" });
    expect(created.map((n) => [n.title, n.closed])).toEqual([["first", true], ["second", false]]);
    expect(created[1].options?.tag).toBe("attention:T2");

    created[1].onclick?.();
    expect(clicks).toEqual([{ id: "attention:T2", tabId: "T2" }]);
    expect(created[1].closed).toBe(true);
  });
});
