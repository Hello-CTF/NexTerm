/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: () => Promise.resolve(() => {}) };
});

import { useUi } from "../../app/store";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import type { TerminalAttentionEvent } from "../../ipc/events";
import { InboxButton } from "../../features/inbox/InboxButton";
import { resetInboxGuidanceForTest } from "../../features/inbox/firstRunGuidance";
import { setSystemNotifierForTest, type SystemNotifier } from "../../features/inbox/systemNotifications";
import { onSettingsSection } from "../../features/settings/settingsNavigation";

let version = 0;
function attention(tabId: string): TerminalAttentionEvent {
  version += 1;
  return { tabId, state: "needs_me", confidence: "signal", source: "helper", reason: "bell", since: "2026-10-27T10:00:00Z", version };
}

const request = vi.fn();
let permission: "granted" | "prompt" = "prompt";

function fakeNotifier(): SystemNotifier {
  return {
    status: () => Promise.resolve({ available: true, permission }),
    knownStatus: () => ({ available: true, permission }),
    request: () => {
      request();
      permission = "granted";
      return Promise.resolve({ available: true, permission });
    },
    show: () => Promise.resolve(),
    remove: () => {},
    onClick: () => {},
  };
}

const guidance = (view: MountedView) => view.container.querySelector("[data-inbox-guidance]");
const bell = (view: MountedView) => view.container.querySelector('button[aria-haspopup="dialog"]') as Element;

describe("inbox first-run guidance", () => {
  let view: MountedView | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
    resetInboxGuidanceForTest();
    resetAwarenessStore();
    request.mockClear();
    permission = "prompt";
    setSystemNotifierForTest(fakeNotifier());
    useUi.setState({ workspaces: [], activeWorkspaceId: null });
  });

  afterEach(() => {
    view?.unmount();
    view = undefined;
    setSystemNotifierForTest(null);
    resetAwarenessStore();
    vi.useRealTimers();
  });

  it("shows a cue at the first count without opening the list or moving focus", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    view = mount(createElement(InboxButton));
    expect(view.container.querySelector("[data-inbox-cue]")).toBeNull();

    act(() => applyAwareness([attention("T1")]));
    expect(view.container.querySelector("[data-inbox-cue]")).not.toBeNull();
    expect(view.container.querySelector('[role="status"]')?.textContent).toContain("新");
    expect(view.container.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(input);

    act(() => {
      vi.advanceTimersByTime(8000);
    });
    expect(view.container.querySelector("[data-inbox-cue]")).toBeNull();
    input.remove();
  });

  it("clears the cue on click and shows the card when the user opens the list", () => {
    view = mount(createElement(InboxButton));
    act(() => applyAwareness([attention("T1")]));
    expect(guidance(view)).toBeNull();
    click(bell(view));
    expect(view.container.querySelector("[data-inbox-cue]")).toBeNull();
    expect(view.container.querySelector('[role="dialog"]')).not.toBeNull();
    expect(guidance(view)).not.toBeNull();
  });

  it("is not shown again after it was dismissed, including after a reload", () => {
    act(() => applyAwareness([attention("T1")]));
    view = mount(createElement(InboxButton));
    click(bell(view));
    click(view.container.querySelector('[data-guidance="dismiss"]') as Element);
    expect(guidance(view)).toBeNull();
    view.unmount();

    resetInboxGuidanceForTest();
    view = mount(createElement(InboxButton));
    expect(view.container.querySelector("[data-inbox-cue]")).toBeNull();
    click(bell(view));
    expect(view.container.querySelector('[role="dialog"]')).not.toBeNull();
    expect(guidance(view)).toBeNull();
  });

  it("asks for the system notification permission from the enable button", async () => {
    act(() => applyAwareness([attention("T1")]));
    view = mount(createElement(InboxButton));
    click(bell(view));
    click(view.container.querySelector('[data-guidance="enable"]') as Element);
    await flush();
    expect(request).toHaveBeenCalledTimes(1);
    // Once allowed the guidance has done its job.
    expect(guidance(view)).toBeNull();
  });

  it("offers no enable button when the permission is already granted", () => {
    permission = "granted";
    act(() => applyAwareness([attention("T1")]));
    view = mount(createElement(InboxButton));
    click(bell(view));
    expect(guidance(view)).not.toBeNull();
    expect(view.container.querySelector('[data-guidance="enable"]')).toBeNull();
  });

  it("opens the terminal settings section from the settings link", () => {
    act(() => applyAwareness([attention("T1")]));
    view = mount(createElement(InboxButton));
    click(bell(view));
    const sections: string[] = [];
    const off = onSettingsSection((id) => sections.push(id));
    click(view.container.querySelector('[data-guidance="settings"]') as Element);
    off();
    expect(sections).toEqual(["settings-terminal"]);
    expect(view.container.querySelector('[role="dialog"]')).toBeNull();
    expect(guidance(view)).toBeNull();
  });
});
