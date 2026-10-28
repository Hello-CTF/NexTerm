/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import { ProjectWorkspaceStrip } from "../../app/ProjectWorkspaceStrip";
import { projectFocusForGroup, type ProjectInventory } from "../../app/projectFocus";
import {
  aggregateAttention,
  applyAwareness,
  resetAwarenessStore,
  toAwareness,
} from "../../app/sessionAwarenessStore";
import type { AppTab, Workspace } from "../../app/store";
import type { TerminalAttentionEvent } from "../../ipc/events";

function event(tabId: string, patch: Partial<TerminalAttentionEvent> = {}): TerminalAttentionEvent {
  return { tabId, state: "needs_me", confidence: "signal", source: "helper", since: "2026-10-27T10:00:00Z", version: 1, ...patch };
}

function term(id: string, patch: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, tabId: `k-${id}`, closable: true, ...patch };
}

function workspace(id: string, tabs: AppTab[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `s-${id}`,
    assetId: `asset-${id}`,
    assetKind: "ssh",
    panes: [
      { id: `${id}-p1`, tabs: tabs.slice(0, 1), activeTabId: tabs[0]?.id ?? null },
      { id: `${id}-p2`, tabs: tabs.slice(1), activeTabId: tabs[1]?.id ?? null },
    ],
    activePaneId: `${id}-p1`,
    closable: true,
  };
}

const inventory: ProjectInventory = {
  groups: [{ id: "p1", parentId: null, name: "P1", sort: 0, createdAt: 1, updatedAt: 1 }],
  assets: ["a", "b", "c"].map((name) => ({
    id: `asset-${name}`, groupId: "p1", kind: "ssh", name, host: null, port: null, username: null, authKind: null,
    keyPath: null, credId: null, options: {}, tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null, builtin: false,
  })),
};

const workspaces = [
  workspace("a", [term("a1"), term("a2")]),
  workspace("b", [term("b1"), term("b2", { exited: true })]),
  workspace("c", [term("c1")]),
];

let mounted: MountedView | undefined;

function renderStrip() {
  mounted = mount(createElement(ProjectWorkspaceStrip, {
    workspaces,
    activeWorkspaceId: "c",
    activeProject: projectFocusForGroup("p1"),
    projectInventory: inventory,
    groups: inventory.groups,
    onSelect: () => {},
  }));
}

function badgeOf(workspaceId: string): HTMLElement | null {
  return mounted?.container.querySelector(`[data-project-workspace-id="${workspaceId}"] [data-attention]`) ?? null;
}

beforeEach(() => {
  document.body.replaceChildren();
  resetAwarenessStore();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("aggregateAttention", () => {
  const map = (...events: TerminalAttentionEvent[]) => new Map(events.map((item) => [item.tabId, toAwareness(item)]));

  it("prefers needs_me over just_finished, whatever the tab order", () => {
    const states = map(event("x", { state: "just_finished", exitCode: 0 }), event("y"));
    expect(aggregateAttention(states, ["x", "y"])?.kind).toBe("needs_me");
    expect(aggregateAttention(states, ["y", "x"])?.kind).toBe("needs_me");
  });

  it("ignores running, unknown, idle and seen states", () => {
    const states = map(
      event("r", { state: "running" }),
      event("u", { state: "unknown" }),
      event("i", { state: "idle" }),
      event("s", { seen: true }),
    );
    expect(aggregateAttention(states, ["r", "u", "i", "s", "missing"])).toBeNull();
  });
});

describe("workspace strip attention badge", () => {
  it("shows the highest-priority unseen state across every pane of the workspace", () => {
    act(() => applyAwareness([
      event("k-a1", { state: "just_finished", exitCode: 0 }),
      event("k-a2"),
      event("k-b1", { state: "just_finished", exitCode: 0 }),
    ]));
    renderStrip();
    expect(badgeOf("a")?.getAttribute("data-attention")).toBe("needs_me");
    expect(badgeOf("b")?.getAttribute("data-attention")).toBe("just_finished");
    expect(badgeOf("c")).toBeNull();
  });

  it("never shows running, and ignores exited tabs", () => {
    act(() => applyAwareness([event("k-a1", { state: "running" }), event("k-b2")]));
    renderStrip();
    expect(badgeOf("a")).toBeNull();
    expect(badgeOf("b")).toBeNull();
  });

  it("updates live and clears once the state is seen", () => {
    renderStrip();
    expect(badgeOf("c")).toBeNull();
    act(() => applyAwareness([event("k-c1", { version: 1 })]));
    expect(badgeOf("c")?.getAttribute("data-attention")).toBe("needs_me");
    act(() => applyAwareness([event("k-c1", { version: 2, seen: true })]));
    expect(badgeOf("c")).toBeNull();
  });
});
