/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { sanitizeLayout, serializeLayout } from "../../app/layout";
import type { AppTab, Workspace } from "../../app/store";

describe("browser tabs in a saved layout", () => {
  it("are not restored and leave a loadable pane", () => {
    const tabs: AppTab[] = [
      { id: "b1", kind: "browser", title: "example.com", url: "https://example.com/", closable: true },
      { id: "f2", kind: "files", title: "a.txt", sessionId: "s1", path: "/tmp/a.txt", closable: true },
    ];
    const workspace: Workspace = {
      id: "ws1",
      kind: "session",
      title: "web-01",
      sessionId: "s1",
      panes: [{ id: "p1", tabs, activeTabId: "b1" }],
      activePaneId: "p1",
      closable: true,
    };
    const saved = serializeLayout({
      leftOpen: true,
      leftMode: "assets",
      rightOpen: false,
      leftWidth: 260,
      rightWidth: 360,
      workspaces: [workspace],
      activeWorkspaceId: "ws1",
      aiBoards: {},
    } as Parameters<typeof serializeLayout>[0]);
    expect(JSON.stringify(saved)).not.toContain("example.com");
    const pane = sanitizeLayout(JSON.parse(JSON.stringify(saved)))?.workspaces[0]?.panes[0];
    expect(pane?.tabs.map((tab) => tab.kind)).toEqual(["files"]);
    expect(pane?.tabs.some((tab) => tab.id === pane.activeTabId)).toBe(true);
  });
});
