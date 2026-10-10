/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { findWritableTerminal, useUi, type AppTab } from "../../app/store";

function terminal(id: string, patch: Partial<AppTab> = {}): AppTab {
  return {
    id,
    kind: "terminal",
    title: id,
    sessionId: "s1",
    tabId: `kernel-${id}`,
    closable: true,
    ...patch,
  };
}

function seed(tabs: AppTab[]): void {
  useUi.setState({
    workspaces: [
      {
        id: "ws",
        kind: "session",
        title: "web-01",
        sessionId: "s1",
        panes: [{ id: "pane", tabs, activeTabId: tabs[0]?.id ?? null }],
        activePaneId: "pane",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws",
    sessions: [],
    toasts: [],
  });
}

describe("findWritableTerminal", () => {
  it("skips dead and exited terminals, including the active tab", () => {
    seed([
      terminal("dead", { dead: true }),
      terminal("exited", { exited: true }),
      terminal("live"),
    ]);

    expect(findWritableTerminal("s1")).toBe("kernel-live");
  });

  it("returns null when every terminal is dead or exited", () => {
    seed([terminal("dead", { dead: true }), terminal("exited", { exited: true })]);

    expect(findWritableTerminal("s1")).toBeNull();
  });
});
