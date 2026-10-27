/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import type { AppTab } from "../../app/store";
import type { TerminalAttentionEvent } from "../../ipc/events";
import { stateMarkItems } from "../../features/session/stateMarkMenu";
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
    expect(items.map((item) => (item.kind === "item" ? item.label : ""))).toEqual(["状态不对…", "这就是需要我…"]);
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
    expect(items[0]).toMatchObject({ kind: "item", label: "状态不对…", disabled: true, hint: "状态规则只作用于持久终端" });
  });

  it("offers nothing for unknown states", () => {
    applyAwareness([attention("unknown", "helper")]);
    expect(stateMarkItems(tab, "backend-1")).toEqual([]);
    resetAwarenessStore();
    expect(stateMarkItems(tab, "backend-1")).toEqual([]);
  });
});
