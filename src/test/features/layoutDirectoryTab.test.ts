/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { sanitizeLayout, serializeLayout } from "../../app/layout";
import type { AppTab, Workspace } from "../../app/store";

function layoutWith(tabs: AppTab[]) {
  const workspace: Workspace = {
    id: "ws1",
    kind: "session",
    title: "web-01",
    sessionId: "s1",
    panes: [{ id: "p1", tabs, activeTabId: tabs[0].id }],
    activePaneId: "p1",
    closable: true,
  };
  return serializeLayout({
    leftOpen: true,
    leftMode: "assets",
    rightOpen: false,
    leftWidth: 260,
    rightWidth: 360,
    workspaces: [workspace],
    activeWorkspaceId: "ws1",
    aiBoards: {},
  } as Parameters<typeof serializeLayout>[0]);
}

describe("legacy file browser tabs in a saved layout", () => {
  it("are dropped quietly while file editor tabs survive", () => {
    const saved = layoutWith([
      { id: "f1", kind: "files", title: "nginx", sessionId: "s1", closable: true },
      { id: "f2", kind: "files", title: "a.txt", sessionId: "s1", path: "/tmp/a.txt", closable: true },
      { id: "f3", kind: "files", title: "文件", sessionId: "s1", closable: true },
    ]);
    // An older build also saved a directory on browser tabs.
    const raw = JSON.parse(JSON.stringify(saved));
    raw.workspaces[0].panes[0].tabs[0].directory = "/var/log/nginx";
    const reloaded = sanitizeLayout(raw);
    const pane = reloaded?.workspaces[0]?.panes[0];
    expect(pane?.tabs.map((tab) => [tab.id, tab.path])).toEqual([["f2", "/tmp/a.txt"]]);
    expect(pane?.tabs.some((tab) => tab.id === pane.activeTabId)).toBe(true);
  });

  it("leave a workspace with only a browser tab loadable", () => {
    const saved = layoutWith([{ id: "f1", kind: "files", title: "文件", sessionId: "s1", closable: true }]);
    expect(() => sanitizeLayout(JSON.parse(JSON.stringify(saved)))).not.toThrow();
  });
});
