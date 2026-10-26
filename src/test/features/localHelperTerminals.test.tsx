/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  closeTab: vi.fn(),
  status: vi.fn(),
  restart: vi.fn(),
  retry: vi.fn(),
  listSessions: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable, closeTab: mocks.closeTab },
    sessionApi: { list: mocks.listSessions },
    localHelperApi: { status: mocks.status, restart: mocks.restart, retry: mocks.retry },
  });
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { backgroundTargets, type BackgroundScope } from "../../features/terminal/backgroundSessionsAccess";
import { localHelperBadge, setLocalHelperStatusForTest } from "../../features/terminal/localHelperStatus";
import { LocalHelperSettings } from "../../features/settings/LocalHelperSettings";
import { getTerminalPrefs, setTerminalLocalMode } from "../../app/preferences";
import { useUi, type Workspace } from "../../app/store";
import { useAuth } from "../../features/auth/store";
import { flush, mount, waitFor } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";

function session(kind: string, id: string): SessionInfo {
  return { id, assetId: `${kind}-asset`, name: kind === "local" ? "本机" : id, kind, status: "connected", tabs: [], createdAt: 0 };
}

const localWorkspace: Workspace = {
  id: "ws-local",
  kind: "session",
  title: "本机",
  sessionId: "local-1",
  assetId: "local-asset",
  assetKind: "local",
  panes: [{ id: "pane", tabs: [], activeTabId: null }],
  activePaneId: "pane",
  closable: true,
};

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  useAuth.setState({ user: null });
  setLocalHelperStatusForTest(null);
  mocks.ask.mockResolvedValue(true);
  mocks.listLive.mockResolvedValue([]);
  mocks.listDurable.mockResolvedValue([]);
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.status.mockResolvedValue({ state: "ready" });
  mocks.listSessions.mockResolvedValue([session("local", "local-1")]);
});

describe("local terminals in the local helper", () => {
  it("lists local sessions as durable hosts only when a local helper runs", () => {
    const scope: BackgroundScope = { sessions: [session("local", "local-1"), session("ssh", "ssh-1")], workspaces: [], verified: {} };
    const withoutHelper = backgroundTargets(scope, { showAll: true, hostSessionId: null, remoteOwner: false });
    expect(withoutHelper.map((item) => item.id)).toEqual(["ssh-1"]);
    const withHelper = backgroundTargets({ ...scope, localDurable: true }, { showAll: true, hostSessionId: null, remoteOwner: false });
    expect(withHelper.map((item) => item.id)).toEqual(["local-1", "ssh-1"]);
  });

  it("marks a regular local terminal with the helper state", () => {
    expect(localHelperBadge(null)).toBeNull();
    expect(localHelperBadge({ state: "disabled" })).toBeNull();
    expect(localHelperBadge({ state: "ready" })).toEqual({ state: "ready" });
    expect(localHelperBadge({ state: "starting" })?.state).toBe("installing");
    expect(localHelperBadge({ state: "outdated", reason: "旧版本" })).toEqual({ state: "attention", message: "旧版本" });
  });

  it("keeps the local terminal mode as a device preference", () => {
    expect(getTerminalPrefs().localMode).toBe("durable");
    setTerminalLocalMode("regular");
    expect(getTerminalPrefs().localMode).toBe("regular");
    expect(window.localStorage.getItem("nexterm.terminalClose.v1")).toContain('"localMode":"regular"');
    setTerminalLocalMode("durable");
  });

  it("shows background local terminals from the helper and terminates them on request", async () => {
    setLocalHelperStatusForTest({ state: "ready" });
    useUi.setState({ workspaces: [localWorkspace], activeWorkspaceId: "ws-local", sessions: [session("local", "local-1")], toasts: [] });
    mocks.listDurable.mockResolvedValue([{ id: "local-shell", cols: 80, rows: 24, dead: false, title: "构建任务" }]);
    const view = mount(createElement(BackgroundSessions, { workspaceId: "ws-local" }));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建任务"));
      expect(mocks.listDurable).toHaveBeenCalledWith("local-1");
      const kill = view.container.querySelector<HTMLButtonElement>('button[title="结束这个终端里运行的程序，无法恢复"]');
      expect(kill).not.toBeNull();
      await act(async () => kill!.click());
      await flush();
      expect(mocks.ask).toHaveBeenCalled();
      expect(mocks.closeTab).toHaveBeenCalledWith("local-shell", "kill", "local-1");
    } finally {
      view.unmount();
    }
  });

  it("does not ask a disabled local helper for terminals", async () => {
    setLocalHelperStatusForTest({ state: "disabled" });
    useUi.setState({ workspaces: [localWorkspace], activeWorkspaceId: "ws-local", sessions: [session("local", "local-1")], toasts: [] });
    const view = mount(createElement(BackgroundSessions, { workspaceId: "ws-local" }));
    try {
      await flush();
      expect(mocks.listDurable).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("restarts an outdated helper only after the user confirms ending its sessions", async () => {
    setLocalHelperStatusForTest({ state: "outdated", reason: "本机持久组件为旧版本", activeSessions: 2, consentToken: "3:42:2" });
    mocks.restart.mockResolvedValue({ state: "ready" });
    const view = mount(createElement(LocalHelperSettings));
    try {
      await flush();
      expect(view.container.textContent).toContain("本机持久组件为旧版本");
      const restart = [...view.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("结束 2 个会话"));
      expect(restart).toBeDefined();
      mocks.ask.mockResolvedValueOnce(false);
      await act(async () => restart!.click());
      await flush();
      expect(mocks.restart).not.toHaveBeenCalled();
      expect(String(mocks.ask.mock.calls[0][0])).toContain("2 个会话");
      mocks.ask.mockResolvedValueOnce(true);
      await act(async () => restart!.click());
      await flush();
      expect(mocks.restart).toHaveBeenCalledWith("3:42:2");
    } finally {
      view.unmount();
    }
  });
});
