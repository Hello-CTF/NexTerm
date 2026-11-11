/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import type { AppTab } from "../../app/store";
import type { TerminalAttentionEvent } from "../../ipc/events";
import { muteItem, stateMarkItems } from "../../features/session/stateMarkMenu";
import { isSessionMuted, setSessionMuted } from "../../app/mutedSessions";
import { onStateMarkRequest } from "../../features/session/stateMarkRequests";

const tab: AppTab = { id: "app-1", kind: "terminal", title: "agent", tabId: "backend-1", assetId: "A1", closable: true };

function attention(state: string, source: string): TerminalAttentionEvent {
  return { tabId: "backend-1", state, source, version: 1 } as TerminalAttentionEvent;
}

afterEach(() => resetAwarenessStore());

describe("state marking menu items", () => {
  it("opens the marking dialog for a helper tab with the shown state", () => {
    applyAwareness([attention("running", "helper")]);
    const items = stateMarkItems(tab, "backend-1");
    expect(items.map((item) => (item.kind === "item" ? item.label : ""))).toEqual(["状态有误…", "标记为需要处理…"]);
    const listener = vi.fn();
    const off = onStateMarkRequest(listener);
    const thisIsIt = items[1];
    if (thisIsIt.kind === "item") thisIsIt.onSelect?.();
    off();
    expect(listener).toHaveBeenCalledWith({ appTabId: "app-1", tabId: "backend-1", assetId: "A1", title: "agent", intent: "this_is_it", current: "running" });
  });

  it("shows the items disabled with a reason where rules cannot apply", () => {
    applyAwareness([attention("needs_me", "observed")]);
    const items = stateMarkItems(tab, "backend-1");
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "item", label: "状态有误…", disabled: true, hint: "状态规则只作用于持久终端" });
  });

  it("offers nothing for unknown states", () => {
    applyAwareness([attention("unknown", "helper")]);
    expect(stateMarkItems(tab, "backend-1")).toEqual([]);
    resetAwarenessStore();
    expect(stateMarkItems(tab, "backend-1")).toEqual([]);
  });
});

describe("mute menu item", () => {
  it("toggles the session's mute", async () => {
    const tabId = "e".repeat(32);
    const first = muteItem(tabId);
    expect(first).toMatchObject({ kind: "item", label: "静音此会话" });
    if (first.kind === "item") first.onSelect?.();
    await vi.waitFor(() => expect(isSessionMuted(tabId)).toBe(true));
    const second = muteItem(tabId);
    expect(second).toMatchObject({ kind: "item", label: "取消静音" });
    if (second.kind === "item") second.onSelect?.();
    await vi.waitFor(() => expect(isSessionMuted(tabId)).toBe(false));
    await setSessionMuted(tabId, false);
  });
});
