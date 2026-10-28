/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, Fragment } from "react";
import { act } from "react";
import { mount } from "./reactTestUtils";
import { ATTENTION_HOLD_MS, TabAttentionBadge, TabStripItem, tabTooltip } from "../../app/workspace/tabStrip";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import type { AppTab } from "../../app/store";
import type { TerminalAttentionEvent } from "../../ipc/events";

let version = 0;

beforeEach(() => {
  vi.useFakeTimers();
  resetAwarenessStore();
});

afterEach(() => {
  vi.useRealTimers();
  resetAwarenessStore();
});

function terminal(patch: Partial<AppTab> = {}): AppTab {
  return { id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", tabId: "T1", closable: true, ...patch };
}

function report(patch: Partial<TerminalAttentionEvent>): void {
  version += 1;
  applyAwareness([{ tabId: "T1", state: "idle", confidence: "signal", source: "helper", since: "2026-10-27T10:00:00Z", version, ...patch }]);
}

// renderTab mounts the tab strip pieces the way the pane tab bar does and returns the badge and the tooltip.
function renderTab(tab: AppTab, active = false, wait = ATTENTION_HOLD_MS): { badge: Element | null; title: string } {
  let title = "";
  const view = mount(
    createElement(TabStripItem, {
      tab,
      active,
      children: (unread: boolean, persistence, attention) => {
        title = tabTooltip(tab, unread, persistence, attention);
        return createElement(Fragment, null, createElement(TabAttentionBadge, { badge: attention }));
      },
    }),
  );
  act(() => {
    vi.advanceTimersByTime(wait);
  });
  // Read the result and unmount, so later store updates do not reach a mounted view outside act.
  const badge = view.container.querySelector('[role="img"]')?.cloneNode(true) as Element | undefined;
  view.unmount();
  return { badge: badge ?? null, title };
}

describe("tab attention badge", () => {
  it("marks a terminal that needs its user", () => {
    report({ state: "needs_me" });
    const signalled = renderTab(terminal());
    expect(signalled.badge?.getAttribute("aria-label")).toBe("需要处理");
    expect(signalled.title).toContain("需要处理");
  });

  it("shows a grey question mark instead of an old state when the state is unknown", () => {
    report({ state: "needs_me" });
    report({ state: "unknown", confidence: undefined, reason: "disconnected" });
    const { badge } = renderTab(terminal());
    expect(badge?.textContent).toBe("?");
    expect(badge?.getAttribute("aria-label")).toContain("状态未知");
  });

  it("gives each state its own shape, not only a colour", () => {
    report({ state: "needs_me" });
    expect(renderTab(terminal()).badge?.textContent).toBe("!");
    report({ state: "just_finished", exitCode: 0 });
    const finished = renderTab(terminal()).badge;
    expect(finished?.getAttribute("aria-label")).toBe("命令刚结束，成功");
    expect(finished?.querySelector("svg")).not.toBeNull();
    expect(finished?.textContent).toBe("");
  });

  it("draws a failed command with a cross instead of the green check", () => {
    report({ state: "just_finished", exitCode: 2 });
    const failed = renderTab(terminal()).badge;
    expect(failed?.getAttribute("data-outcome")).toBe("failed");
    expect(failed?.getAttribute("aria-label")).toBe("命令刚结束，退出码 2");
    expect(failed?.className).not.toContain("green");
    expect(failed?.className).not.toContain("red");
    report({ state: "just_finished", exitCode: 0 });
    const ok = renderTab(terminal()).badge;
    expect(ok?.getAttribute("data-outcome")).toBe("ok");
    expect(ok?.className).toContain("green");
    report({ state: "just_finished" });
    expect(renderTab(terminal()).badge?.getAttribute("data-outcome")).toBe("ok");
  });

  it("clears a state seen on any device and keeps idle tabs unmarked", () => {
    report({ state: "just_finished", exitCode: 0, seen: true });
    expect(renderTab(terminal()).badge).toBeNull();
    report({ state: "idle" });
    expect(renderTab(terminal()).badge).toBeNull();
  });

  it("never shows running, and shows no needs_me badge on the tab the user is on", () => {
    report({ state: "running" });
    expect(renderTab(terminal()).badge).toBeNull();
    expect(renderTab(terminal(), true).badge).toBeNull();
    report({ state: "needs_me" });
    expect(renderTab(terminal(), true).badge).toBeNull();
    expect(renderTab(terminal({ exited: true })).badge).toBeNull();
    expect(renderTab({ id: "f1", kind: "files", title: "文件", tabId: "T1", closable: true }).badge).toBeNull();
  });

  it("holds a needs_me badge back until the state has lasted, so a flapping state never flashes", () => {
    report({ state: "needs_me" });
    expect(renderTab(terminal(), false, ATTENTION_HOLD_MS - 1).badge).toBeNull();
    expect(renderTab(terminal()).badge?.getAttribute("data-attention")).toBe("needs_me");
  });

  it("does not change the rendered badge while the state flaps between running and idle", () => {
    const seen: Array<string | null> = [];
    for (const state of ["running", "idle", "running", "idle", "running"] as const) {
      report({ state });
      seen.push(renderTab(terminal()).badge?.getAttribute("data-attention") ?? null);
    }
    expect(new Set(seen)).toEqual(new Set([null]));
  });
});
