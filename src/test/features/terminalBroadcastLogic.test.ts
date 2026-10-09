/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AppTab, Workspace } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import {
  broadcastInput,
  broadcastRiskMessage,
  collectBroadcastCandidates,
  describeBroadcastSkips,
  localRemoteMix,
  quickBroadcastIds,
} from "../../features/terminal/broadcast";
import { useUi } from "../../app/store";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn() },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    closeTab: vi.fn().mockResolvedValue(undefined),
  },
}));

function session(id: string, kind: string): SessionInfo {
  return { id, assetId: `a-${id}`, name: id, kind, status: "connected", tabs: [], createdAt: 0 };
}

function termTab(id: string, sessionId: string, patch: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `k-${id}`, closable: true, ...patch };
}

function workspace(panes: { id: string; tabs: AppTab[] }[]): Workspace {
  return {
    id: "ws1",
    kind: "session",
    title: "ws",
    closable: true,
    panes: panes.map((p) => ({ id: p.id, tabs: p.tabs, activeTabId: p.tabs[0]?.id ?? null })),
    activePaneId: panes[0]?.id ?? "",
    splitRatio: 0.5,
  };
}

describe("collectBroadcastCandidates", () => {
  it("marks dead, exited and winrm tabs ineligible with reasons", () => {
    const ws = workspace([
      {
        id: "p1",
        tabs: [
          termTab("t1", "s1"),
          termTab("t2", "s1", { dead: true }),
          termTab("t3", "s1", { exited: true }),
          termTab("t4", "s4"),
          { id: "t5", kind: "files", title: "files", sessionId: "s1", closable: true },
          termTab("t6", "s1", { tabId: undefined }),
        ],
      },
    ]);
    const candidates = collectBroadcastCandidates(ws, [session("s1", "ssh"), session("s4", "winrm")]);
    expect(candidates.map((c) => c.storeTabId)).toEqual(["t1", "t2", "t3", "t4", "t6"]);
    const byId = new Map(candidates.map((c) => [c.storeTabId, c]));
    expect(byId.get("t1")?.eligible).toBe(true);
    expect(byId.get("t2")?.ineligibleReason).toBe("终端已失效");
    expect(byId.get("t3")?.ineligibleReason).toBe("进程已退出");
    expect(byId.get("t4")?.ineligibleReason).toBe("WinRM 非交互终端不支持广播");
    expect(byId.get("t6")?.eligible).toBe(true);
    expect(byId.get("t6")?.kernelTabId).toBeUndefined();
  });

  it("flags local sessions for the mixed-broadcast guard", () => {
    const ws = workspace([{ id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] }]);
    const candidates = collectBroadcastCandidates(ws, [session("s1", "local"), session("s2", "ssh")]);
    expect(candidates.map((c) => c.local)).toEqual([true, false]);
  });
});

describe("quickBroadcastIds", () => {
  const ws = workspace([
    { id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s1", { dead: true })] },
    { id: "p2", tabs: [termTab("t3", "s1")] },
  ]);
  const sessions = [session("s1", "ssh")];

  it("scope=pane only picks eligible tabs of the given pane", () => {
    const candidates = collectBroadcastCandidates(ws, sessions);
    expect(quickBroadcastIds(candidates, "pane", "p1")).toEqual(["t1"]);
  });

  it("scope=workspace picks all eligible tabs", () => {
    const candidates = collectBroadcastCandidates(ws, sessions);
    expect(quickBroadcastIds(candidates, "workspace")).toEqual(["t1", "t3"]);
  });
});

describe("localRemoteMix / broadcastRiskMessage", () => {
  it("detects mixed local+remote selections", () => {
    const ws = workspace([{ id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] }]);
    const candidates = collectBroadcastCandidates(ws, [session("s1", "local"), session("s2", "ssh")]);
    const mix = localRemoteMix(candidates);
    expect(mix).toEqual({ local: 1, remote: 1, mixed: true });
    expect(broadcastRiskMessage(2, mix)).toContain("本地终端（1 个）与远程终端（1 个）");
  });

  it("does not add the mixed warning for pure remote sets", () => {
    const ws = workspace([{ id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] }]);
    const candidates = collectBroadcastCandidates(ws, [session("s1", "ssh"), session("s2", "ssh")]);
    const mix = localRemoteMix(candidates);
    expect(mix.mixed).toBe(false);
    const message = broadcastRiskMessage(2, mix);
    expect(message).toContain("2 个终端");
    expect(message).not.toContain("本地终端");
  });
});

describe("broadcastInput", () => {
  const ws = workspace([
    {
      id: "p1",
      tabs: [
        termTab("t1", "s1"),
        termTab("t2", "s1"),
        termTab("t3", "s1", { dead: true }),
        termTab("t4", "s1", { tabId: undefined }),
      ],
    },
  ]);
  const sessions = [session("s1", "ssh")];
  const candidates = collectBroadcastCandidates(ws, sessions);

  it("writes the same bytes to every eligible target and reports skips", async () => {
    const write = vi.fn().mockResolvedValue(undefined);
    const outcome = await broadcastInput({
      candidates,
      targetIds: ["t1", "t2", "t3", "t4", "gone"],
      data: "ls\n",
      write,
    });
    expect(write).toHaveBeenCalledTimes(2);
    const calls = write.mock.calls as [string, Uint8Array][];
    expect(calls.map(([id]) => id)).toEqual(["k-t1", "k-t2"]);
    expect(new TextDecoder().decode(calls[0]?.[1])).toBe("ls\n");
    expect(outcome.sent).toBe(2);
    expect(outcome.skipped).toEqual([
      { title: "t3", reason: "终端已失效" },
      { title: "t4", reason: "终端尚未连接" },
      { title: "gone", reason: "标签已关闭" },
    ]);
  });

  it("maps not_controller write failures to the observer skip reason", async () => {
    const write = vi
      .fn()
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce({ code: "not_controller" });
    const outcome = await broadcastInput({
      candidates,
      targetIds: ["t1", "t2"],
      data: "x",
      write,
    });
    expect(outcome.sent).toBe(1);
    expect(outcome.skipped).toEqual([{ title: "t2", reason: "正被其他设备控制" }]);
  });

  it("maps other write failures to a generic failure reason", async () => {
    const write = vi.fn().mockRejectedValue(new Error("boom"));
    const outcome = await broadcastInput({ candidates, targetIds: ["t1"], data: "x", write });
    expect(outcome.sent).toBe(0);
    expect(outcome.skipped).toEqual([{ title: "t1", reason: "写入失败" }]);
  });
});

describe("describeBroadcastSkips", () => {
  it("caps the listing at three entries", () => {
    const text = describeBroadcastSkips([
      { title: "a", reason: "r" },
      { title: "b", reason: "r" },
      { title: "c", reason: "r" },
      { title: "d", reason: "r" },
      { title: "e", reason: "r" },
    ]);
    expect(text).toContain("「a」r");
    expect(text).toContain("等 5 个");
    expect(text).not.toContain("「d」");
  });
});

describe("broadcast store cleanup", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    useUi.setState({
      sessions: [session("s1", "ssh")],
      workspaces: [
        workspace([
          { id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s1")] },
          { id: "p2", tabs: [termTab("t3", "s1")] },
        ]),
      ],
      activeWorkspaceId: "ws1",
      broadcast: null,
    });
  });

  it("defaults to off", () => {
    useUi.setState({ broadcast: null });
    expect(useUi.getState().broadcast).toBeNull();
  });

  it("prunes closed tabs from the broadcast set and turns off when empty", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    await useUi.getState().closeTab("t2");
    expect(useUi.getState().broadcast).toEqual({ workspaceId: "ws1", targetIds: ["t1"] });
    await useUi.getState().closeTab("t1");
    expect(useUi.getState().broadcast).toBeNull();
  });

  it("keeps the broadcast set when an unrelated tab closes", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    await useUi.getState().closeTab("t3");
    expect(useUi.getState().broadcast).toEqual({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
  });

  it("clears the broadcast set when its workspace closes", async () => {
    useUi.getState().setBroadcast({ workspaceId: "ws1", targetIds: ["t1", "t2"] });
    await useUi.getState().closeWorkspace("ws1");
    expect(useUi.getState().broadcast).toBeNull();
  });
});
