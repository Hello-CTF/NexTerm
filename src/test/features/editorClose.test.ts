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
