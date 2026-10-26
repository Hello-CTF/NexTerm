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

describe("directory file browser tabs in the layout", () => {
  it("keep their directory across a save and reload", () => {
    const saved = layoutWith([
      { id: "f1", kind: "files", title: "nginx", sessionId: "s1", directory: "/var/log/nginx", closable: true },
      { id: "f2", kind: "files", title: "a.txt", sessionId: "s1", path: "/tmp/a.txt", directory: "/tmp", closable: true },
      { id: "f3", kind: "files", title: "文件", sessionId: "s1", closable: true },
    ]);
    const reloaded = sanitizeLayout(JSON.parse(JSON.stringify(saved)));
    const tabs = reloaded?.workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.map((tab) => [tab.id, tab.path, tab.directory])).toEqual([
      ["f1", undefined, "/var/log/nginx"],
      // An editor tab is a file, never a directory browser.
      ["f2", "/tmp/a.txt", undefined],
      ["f3", undefined, undefined],
    ]);
  });
});
