/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  layoutApi: {},
  terminalApi: {},
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import { sanitizeLayout, serializeLayout } from "../../app/layout";
import type { Workspace } from "../../app/store";

const workspace: Workspace = {
  id: "ws",
  kind: "session",
  title: "web-01",
  sessionId: "s1",
  panes: [
    {
      id: "pane",
      tabs: [
        {
          id: "log-1",
          kind: "log",
          title: "app.log",
          sessionId: "s1",
          path: "/var/log/app.log",
          closable: true,
        },
      ],
      activeTabId: "log-1",
    },
  ],
  activePaneId: "pane",
  splitRatio: 0.5,
  closable: true,
};

describe("log tab layout serialization", () => {
  it("serialize -> JSON -> sanitize round-trip keeps the log tab", () => {
    const serialized = serializeLayout({
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [workspace],
      activeWorkspaceId: "ws",
      layoutPresets: [],
    });

    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(serialized)));
    const pane = parsed?.workspaces[0]?.panes[0];
    expect(pane?.activeTabId).toBe("log-1");
    expect(pane?.tabs).toHaveLength(1);
    expect(pane?.tabs[0]).toMatchObject({
      id: "log-1",
      kind: "log",
      title: "app.log",
      sessionId: "s1",
      path: "/var/log/app.log",
      closable: true,
    });
  });
});
