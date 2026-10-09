/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { closeTab: mocks.closeTab },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { requestCloseTab, useUi } from "../../app/store";
import { setFileEditorDirty } from "../../features/files/editorGuards";

function editor(id = "editor"): AppTab {
  return { id, kind: "files", title: "a.txt", sessionId: "s", path: "/a.txt", closable: true };
}
function terminal(id = "terminal"): AppTab {
  return { id, kind: "terminal", title: "term", sessionId: "s", tabId: "kernel", closable: true };
}
function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

describe("dirty editor close safeguards", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setFileEditorDirty("s", "/a.txt", false);
    mocks.ask.mockResolvedValue(false);
  });

  it("keeps a dirty tab when the user cancels", async () => {
    const tab = editor();
    useUi.setState({ workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])], activeWorkspaceId: "ws" });
    setFileEditorDirty("s", "/a.txt", true);
    await requestCloseTab(tab.id);
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
    expect(mocks.closeTab).not.toHaveBeenCalled();
  });

  it("cancels a whole workspace before any terminal is reclaimed", async () => {
    const tabs = [editor(), terminal()];
    useUi.setState({ workspaces: [ws([{ id: "p", tabs, activeTabId: tabs[0].id }])], activeWorkspaceId: "ws" });
    setFileEditorDirty("s", "/a.txt", true);
    await useUi.getState().closeWorkspace("ws");
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(mocks.askChoice).not.toHaveBeenCalled();
  });

  it("cancels unsplit before closing its dirty pane or terminal", async () => {
    const panes = [
      { id: "p1", tabs: [terminal("other")], activeTabId: "other" },
      { id: "p2", tabs: [editor(), terminal()], activeTabId: "editor" },
    ];
    useUi.setState({ workspaces: [ws(panes)], activeWorkspaceId: "ws" });
    setFileEditorDirty("s", "/a.txt", true);
    await useUi.getState().unsplitWorkspace("p2", "ws");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(2);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(mocks.askChoice).not.toHaveBeenCalled();
  });

  it("closes clean editors without a dirty prompt", async () => {
    const tab = editor();
    useUi.setState({ workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])], activeWorkspaceId: "ws" });
    await requestCloseTab(tab.id);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
  });
});

describe("workspace close mixed summary", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setFileEditorDirty("s", "/a.txt", false);
  });

  it("merges dirty editors and blocked terminals into one summary confirmation", async () => {
    const tabs = [editor(), terminal()];
    useUi.setState({ workspaces: [ws([{ id: "p", tabs, activeTabId: tabs[0].id }])], activeWorkspaceId: "ws" });
    setFileEditorDirty("s", "/a.txt", true);
    mocks.ask.mockResolvedValue(true);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0] as [string, { title: string }];
    expect(message).toContain("1 个文件尚未保存");
    expect(message).toContain("a.txt");
    expect(message).toContain("1 个其他终端不支持转入后台");
    expect(message).toContain("仍要继续？");
    expect(options.title).toBe("关闭「workspace」");
    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel", "kill");
    expect(useUi.getState().workspaces).toHaveLength(0);
  });

  it("cancelling the mixed summary keeps the workspace and issues no IPC", async () => {
    const tabs = [editor(), terminal()];
    useUi.setState({ workspaces: [ws([{ id: "p", tabs, activeTabId: tabs[0].id }])], activeWorkspaceId: "ws" });
    setFileEditorDirty("s", "/a.txt", true);
    mocks.ask.mockResolvedValue(false);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual(tabs);
    expect(mocks.closeTab).not.toHaveBeenCalled();
  });
});
