/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";
import type { AppTab, Workspace } from "../../app/store";
import { terminalApi, type SessionInfo } from "../../ipc/commands";
import {
  BroadcastInputQueue,
  broadcastInput,
  broadcastRiskMessage,
  collectBroadcastCandidates,
  describeBroadcastSkips,
  getBroadcastConfirmationChallenge,
  groupBroadcastCandidates,
  localRemoteMix,
  quickBroadcastIds,
  type BroadcastCandidate,
  type BroadcastTargetResult,
  type BroadcastWrite,
} from "../../features/terminal/broadcast";
import { useUi } from "../../app/store";
import { cleanupDeviceSessions } from "../../features/fleet/deviceSessionCleanup";
import { setTerminalCloseAction } from "../../app/preferences";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn().mockResolvedValue([]) },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    listDurable: vi.fn().mockResolvedValue([]),
    prepareBroadcast: vi.fn().mockResolvedValue(undefined),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    closeTab: vi.fn().mockResolvedValue(undefined),
  },
}));

function session(id: string, kind: string, status: SessionInfo["status"] = "connected"): SessionInfo {
  return { id, assetId: `a-${id}`, name: id, kind, status, tabs: [], createdAt: 0 };
}

function termTab(id: string, sessionId: string, patch: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `k-${id}`, closable: true, retainable: true, ...patch };
}

function workspace(
  panes: { id: string; tabs: AppTab[] }[],
  id = "ws1",
  title = "ws",
): Workspace {
  return {
    id,
    kind: "session",
    title,
    closable: true,
    panes: panes.map((pane) => ({ id: pane.id, tabs: pane.tabs, activeTabId: pane.tabs[0]?.id ?? null })),
    activePaneId: panes[0]?.id ?? "",
  };
}

function enableBroadcast(targetIds: string[], origin = "t1"): void {
  useUi.getState().setBroadcast({
    origin: { workspaceId: "ws1", storeTabId: origin },
    targetIds,
  });
}

function expectBroadcast(targetIds: string[], origin = "t1"): void {
  expect(useUi.getState().broadcast).toMatchObject({
    origin: { workspaceId: "ws1", storeTabId: origin },
    targetIds,
  });
}

function enqueue(
  queue: BroadcastInputQueue,
  candidates: readonly BroadcastCandidate[],
  targetIds: readonly string[],
  data: string,
  write: BroadcastWrite,
  onProgress?: (result: BroadcastTargetResult) => void,
  revision = 1,
) {
  return queue.enqueue({ candidates, targetIds, data, write, onProgress, revision });
}

function flushTasks(): Promise<void> {
  return flushMacrotask();
}

function gatedBroadcastWrites() {
  const calls: string[] = [];
  const pending: (() => void)[] = [];
  const write: BroadcastWrite = (id, bytes) => {
    calls.push(`${id}:${new TextDecoder().decode(bytes)}`);
    return new Promise<void>((resolve) => pending.push(resolve));
  };
  return { calls, pending, write };
}

describe("collectBroadcastCandidates", () => {
  it("marks dead, exited, disconnected and non-interactive tabs ineligible with reasons", () => {
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
          termTab("t7", "s7"),
        ],
      },
    ]);
    const candidates = collectBroadcastCandidates(
      [ws],
      [session("s1", "ssh"), session("s4", "winrm"), session("s7", "ssh", "disconnected")],
    );
    expect(candidates.map((candidate) => candidate.storeTabId)).toEqual(["t1", "t2", "t3", "t4", "t6", "t7"]);
    const byId = new Map(candidates.map((candidate) => [candidate.storeTabId, candidate]));
    expect(byId.get("t1")?.eligible).toBe(true);
    expect(byId.get("t2")?.ineligibleReason).toBe("终端已失效");
    expect(byId.get("t3")?.ineligibleReason).toBe("进程已退出");
    expect(byId.get("t4")?.ineligibleReason).toBe("WinRM 非交互终端不支持广播");
    expect(byId.get("t6")?.ineligibleReason).toBe("终端尚未连接");
    expect(byId.get("t7")?.ineligibleReason).toBe("主机未连接");
  });

  it("includes connected terminals outside the current target set from two workspaces", () => {
    const ws1 = workspace([{ id: "p1", tabs: [termTab("t1", "s1")] }], "ws1", "workspace-one");
    const ws2 = workspace([{ id: "p2", tabs: [termTab("t2", "s2")] }], "ws2", "workspace-two");
    const candidates = collectBroadcastCandidates([ws1, ws2], [session("s1", "ssh"), session("s2", "ssh")]);
    expect(candidates.map((candidate) => candidate.storeTabId)).toEqual(["t1", "t2"]);
    expect(candidates.every((candidate) => candidate.eligible)).toBe(true);
    expect(candidates.map((candidate) => candidate.workspaceId)).toEqual(["ws1", "ws2"]);

    const groups = groupBroadcastCandidates(candidates);
    expect(groups.map((group) => group.hostName)).toEqual(["s1", "s2"]);
    expect(groups.flatMap((group) => group.workspaces.map((item) => item.workspaceTitle))).toEqual([
      "workspace-one",
      "workspace-two",
    ]);
  });

  it("keeps workspace groups independent for each host in a mixed-session workspace", () => {
    const ws = workspace([
      { id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] },
    ]);
    const groups = groupBroadcastCandidates(
      collectBroadcastCandidates([ws], [session("s1", "ssh"), session("s2", "ssh")]),
    );
    expect(
      groups.map((group) => ({
        host: group.hostName,
        targets: group.workspaces.flatMap((item) => item.candidates.map((candidate) => candidate.storeTabId)),
      })),
    ).toEqual([
      { host: "s1", targets: ["t1"] },
      { host: "s2", targets: ["t2"] },
    ]);
    expect(groups.every((group) => group.workspaces.length === 1)).toBe(true);
  });

  it("flags local sessions for the mixed-broadcast guard", () => {
    const ws = workspace([{ id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] }]);
    const candidates = collectBroadcastCandidates([ws], [session("s1", "local"), session("s2", "ssh")]);
    expect(candidates.map((candidate) => candidate.local)).toEqual([true, false]);
  });
});

describe("quickBroadcastIds", () => {
  const ws = workspace([
    { id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s1", { dead: true })] },
    { id: "p2", tabs: [termTab("t3", "s1")] },
  ]);
  const sessions = [session("s1", "ssh")];

  it("scope=pane only picks eligible tabs of the given pane", () => {
    const candidates = collectBroadcastCandidates([ws], sessions);
    expect(quickBroadcastIds(candidates, "pane", "p1")).toEqual(["t1"]);
  });

  it("scope=all picks every eligible tab across open workspaces", () => {
    const other = workspace([{ id: "p3", tabs: [termTab("t4", "s1")] }], "ws2");
    const candidates = collectBroadcastCandidates([ws, other], sessions);
    expect(quickBroadcastIds(candidates, "all")).toEqual(["t1", "t3", "t4"]);
  });
});

describe("localRemoteMix / broadcastRiskMessage", () => {
  it("detects mixed local+remote selections", () => {
    const ws = workspace([{ id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] }]);
    const candidates = collectBroadcastCandidates([ws], [session("s1", "local"), session("s2", "ssh")]);
    const mix = localRemoteMix(candidates);
    expect(mix).toEqual({ local: 1, remote: 1, mixed: true });
    expect(broadcastRiskMessage(2, mix)).toContain("本地终端（1 个）与远程终端（1 个）");
  });

  it("does not add the mixed warning for pure remote sets", () => {
    const ws = workspace([{ id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] }]);
    const candidates = collectBroadcastCandidates([ws], [session("s1", "ssh"), session("s2", "ssh")]);
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
  const candidates = collectBroadcastCandidates([ws], sessions);

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
    expect(calls.map(([id]) => id).sort()).toEqual(["k-t1", "k-t2"]);
    expect(new TextDecoder().decode(calls[0]?.[1])).toBe("ls\n");
    expect(outcome.sent).toBe(2);
    expect(outcome.failed).toEqual([]);
    expect(outcome.skipped).toEqual([
      { title: "t3", reason: "终端已失效" },
      { title: "t4", reason: "终端尚未连接" },
      { title: "gone", reason: "标签已关闭" },
    ]);
  });

  it("maps not_controller write failures to the observer skip reason", async () => {
    const write = vi.fn().mockImplementation((id: string) =>
      id === "k-t2" ? Promise.reject({ code: "not_controller" }) : Promise.resolve(),
    );
    const outcome = await broadcastInput({ candidates, targetIds: ["t1", "t2"], data: "x", write });
    expect(outcome.sent).toBe(1);
    expect(outcome.skipped).toEqual([{ title: "t2", reason: "正被其他设备控制" }]);
  });

  it("reports other write failures as failed instead of skipped", async () => {
    const write = vi.fn().mockRejectedValue(new Error("boom"));
    const outcome = await broadcastInput({ candidates, targetIds: ["t1"], data: "x", write });
    expect(outcome.sent).toBe(0);
    expect(outcome.skipped).toEqual([]);
    expect(outcome.failed).toEqual([{ title: "t1", reason: "写入失败" }]);
    expect(outcome.results[0]?.status).toBe("failed");
  });
});

describe("BroadcastInputQueue", () => {
  const ws = workspace([
    { id: "p1", tabs: [termTab("t1", "s1"), termTab("t2", "s2")] },
  ]);
  const candidates = collectBroadcastCandidates([ws], [session("s1", "ssh"), session("s2", "ssh")]);

  it("orders simultaneous input per target while allowing targets to start independently", async () => {
    interface PendingWrite {
      id: string;
      data: string;
      resolve: () => void;
    }
    const pending: PendingWrite[] = [];
    const write: BroadcastWrite = (id, bytes) =>
      new Promise<void>((resolve) => {
        pending.push({ id, data: new TextDecoder().decode(bytes), resolve });
      });
    const queue = new BroadcastInputQueue(8);
    const first = enqueue(queue, candidates, ["t1", "t2"], "a", write);
    const second = enqueue(queue, candidates, ["t1", "t2"], "b", write);
    await flushTasks();
    expect(pending.map((item) => [item.id, item.data])).toEqual([["k-t1", "a"], ["k-t2", "a"]]);

    pending[0]?.resolve();
    await flushTasks();
    expect(pending.map((item) => [item.id, item.data])).toEqual([
      ["k-t1", "a"],
      ["k-t2", "a"],
      ["k-t1", "b"],
    ]);
    pending[1]?.resolve();
    await flushTasks();
    expect(pending.map((item) => [item.id, item.data])).toEqual([
      ["k-t1", "a"],
      ["k-t2", "a"],
      ["k-t1", "b"],
      ["k-t2", "b"],
    ]);
    for (const item of [...pending]) item.resolve();
    const outcomes = await Promise.all([first, second]);
    expect(outcomes.map((outcome) => outcome?.sent)).toEqual([2, 2]);
  });

  it("checks the live revision before releasing queued writes", async () => {
    let revision = 1;
    const calls: string[] = [];
    const pending: (() => void)[] = [];
    const write: BroadcastWrite = (_id, bytes) => {
      calls.push(new TextDecoder().decode(bytes));
      return new Promise<void>((resolve) => pending.push(resolve));
    };
    const queue = new BroadcastInputQueue(8);
    const args = {
      candidates,
      targetIds: ["t1"],
      write,
      revision: 1,
      isCurrent: () => revision === 1,
    };
    const first = queue.enqueue({ ...args, data: "a" });
    const second = queue.enqueue({ ...args, data: "b" });
    await flushTasks();
    expect(calls).toEqual(["a"]);
    revision = 2;
    pending[0]?.();
    expect(await Promise.all([first, second])).toEqual([null, null]);
    expect(calls).toEqual(["a"]);
  });

  it("skips queued input when its target disappears without a revision change", async () => {
    let currentCandidates = candidates;
    const calls: string[] = [];
    const pending: (() => void)[] = [];
    const write: BroadcastWrite = (_id, bytes) => {
      calls.push(new TextDecoder().decode(bytes));
      return new Promise<void>((resolve) => pending.push(resolve));
    };
    const queue = new BroadcastInputQueue(8);
    const args = {
      candidates,
      targetIds: ["t1"],
      write,
      revision: 1,
      resolveTarget: (targetId: string) =>
        currentCandidates.find((candidate) => candidate.storeTabId === targetId),
    };
    const first = queue.enqueue({ ...args, data: "a" });
    const second = queue.enqueue({ ...args, data: "b" });
    await flushTasks();
    expect(calls).toEqual(["a"]);
    currentCandidates = [];
    pending[0]?.();
    const [firstOutcome, secondOutcome] = await Promise.all([first, second]);
    expect(firstOutcome?.sent).toBe(1);
    expect(secondOutcome?.results).toEqual([
      { targetId: "t1", title: "t1", status: "skipped", reason: "标签已关闭" },
    ]);
    expect(calls).toEqual(["a"]);
  });

  it("preserves per-target keystroke order with seeded random write latency", async () => {
    let seed = 0x32f10a;
    const randomDelay = () => {
      seed = (seed * 1664525 + 1013904223) >>> 0;
      return seed % 9;
    };
    const completed: Record<string, string[]> = { "k-t1": [], "k-t2": [] };
    const write: BroadcastWrite = (id, bytes) => {
      const data = new TextDecoder().decode(bytes);
      return new Promise<void>((resolve) => {
        setTimeout(() => {
          completed[id]?.push(data);
          resolve();
        }, randomDelay());
      });
    };
    const queue = new BroadcastInputQueue(8);
    const input = ["a", "b", "c", "d", "e"];
    await Promise.all(input.map((data) => enqueue(queue, candidates, ["t1", "t2"], data, write)));
    expect(completed["k-t1"]).toEqual(input);
    expect(completed["k-t2"]).toEqual(input);
  });

  it("lets another target finish while one target fails slowly", async () => {
    let rejectSlow: ((reason: Error) => void) | undefined;
    const progress: BroadcastTargetResult[] = [];
    const write: BroadcastWrite = (id) => {
      if (id === "k-t2") return Promise.resolve();
      return new Promise<void>((_resolve, reject) => {
        rejectSlow = reject;
      });
    };
    const queue = new BroadcastInputQueue(8);
    const outcomePromise = enqueue(
      queue,
      candidates,
      ["t1", "t2"],
      "x",
      write,
      (result) => progress.push(result),
    );
    await flushTasks();
    expect(progress.some((result) => result.targetId === "t2" && result.status === "sent")).toBe(true);
    expect(progress.some((result) => result.targetId === "t1" && result.status === "failed")).toBe(false);
    rejectSlow?.(new Error("boom"));
    const outcome = await outcomePromise;
    expect(outcome?.sent).toBe(1);
    expect(outcome?.failed).toEqual([{ title: "t1", reason: "写入失败" }]);
  });

  it("bounds pending writes for a slow target and reports overflow as skipped", async () => {
    interface PendingWrite {
      resolve: () => void;
    }
    const pending: PendingWrite[] = [];
    const write: BroadcastWrite = () =>
      new Promise<void>((resolve) => {
        pending.push({ resolve });
      });
    const progress: BroadcastTargetResult[] = [];
    const queue = new BroadcastInputQueue(3);
    const outcomes = ["a", "b", "c", "d", "e"].map((data) =>
      enqueue(queue, candidates, ["t1"], data, write, (result) => progress.push(result)),
    );
    await flushTasks();
    expect(pending).toHaveLength(1);
    expect(progress.filter((result) => result.reason === "目标写入积压，本次输入已丢弃")).toHaveLength(2);
    for (let index = 0; index < 3; index += 1) {
      await flushTasks();
      expect(pending).toHaveLength(index + 1);
      pending[index]?.resolve();
    }
    const settled = await Promise.all(outcomes);
    expect(pending).toHaveLength(3);
    expect(settled.slice(0, 3).map((outcome) => outcome?.sent)).toEqual([1, 1, 1]);
    expect(settled.slice(3).map((outcome) => outcome?.skipped[0]?.reason)).toEqual([
      "目标写入积压，本次输入已丢弃",
      "目标写入积压，本次输入已丢弃",
    ]);
  });

  it("does not let releases from a previous epoch free new queue capacity", async () => {
    const pending: { data: string; resolve: () => void }[] = [];
    const progress: BroadcastTargetResult[] = [];
    const write: BroadcastWrite = (_id, bytes) =>
      new Promise<void>((resolve) => {
        pending.push({ data: new TextDecoder().decode(bytes), resolve });
      });
    const queue = new BroadcastInputQueue(2);
    const onProgress = (result: BroadcastTargetResult) => progress.push(result);
    const old1 = enqueue(queue, candidates, ["t1"], "old1", write, onProgress);
    const old2 = enqueue(queue, candidates, ["t1"], "old2", write, onProgress);
    await flushTasks();
    expect(pending.map((item) => item.data)).toEqual(["old1"]);

    queue.reset();
    const new1 = enqueue(queue, candidates, ["t1"], "new1", write, onProgress);
    const new2 = enqueue(queue, candidates, ["t1"], "new2", write, onProgress);
    const new3 = enqueue(queue, candidates, ["t1"], "new3", write, onProgress);
    expect(progress.filter((result) => result.reason === "目标写入积压，本次输入已丢弃")).toHaveLength(1);

    pending[0]?.resolve();
    await flushTasks();
    expect(pending.map((item) => item.data)).toEqual(["old1", "new1"]);
    const new4 = enqueue(queue, candidates, ["t1"], "new4", write, onProgress);
    expect(progress.filter((result) => result.reason === "目标写入积压，本次输入已丢弃")).toHaveLength(2);

    pending[1]?.resolve();
    await flushTasks();
    expect(pending.map((item) => item.data)).toEqual(["old1", "new1", "new2"]);
    pending[2]?.resolve();
    const settled = await Promise.all([old1, old2, new1, new2, new3, new4]);
    expect(settled.slice(0, 2)).toEqual([null, null]);
    expect(settled.slice(2, 4).map((outcome) => outcome?.sent)).toEqual([1, 1]);
    expect(settled.slice(4).map((outcome) => outcome?.skipped[0]?.reason)).toEqual([
      "目标写入积压，本次输入已丢弃",
      "目标写入积压，本次输入已丢弃",
    ]);
  });

  it("blocks every write on needs_confirm and forwards the valid confirmation to the retry", async () => {
    const challengeError = {
      code: "needs_confirm",
      message: "生产环境广播需要显式确认",
      detail: {
        kind: "broadcast_production_confirmation",
        targetSetHash: "a".repeat(64),
        inputHash: "b".repeat(64),
        requiredEnvironmentNames: ["production"],
        productionTargets: [
          { tabId: "k-t1", sessionId: "s1", sessionName: "prod-one", hostname: "prod.internal", environment: "production" },
        ],
      },
    };
    const challenge = getBroadcastConfirmationChallenge(challengeError);
    expect(challenge).not.toBeNull();
    if (!challenge) return;
    const prepare = vi.fn().mockRejectedValueOnce(challengeError).mockResolvedValue(undefined);
    const write = vi.fn().mockResolvedValue(undefined);
    const queue = new BroadcastInputQueue(8);
    const args = { candidates, targetIds: ["t1", "t2"], data: "deploy\n", write, prepare, revision: 1 };

    await expect(queue.enqueue(args)).rejects.toBe(challengeError);
    expect(write).not.toHaveBeenCalled();
    expect(prepare.mock.calls[0][0]).toEqual(["k-t1", "k-t2"]);
    expect(new TextDecoder().decode(prepare.mock.calls[0][1])).toBe("deploy\n");

    const productionConfirmation = {
      confirmed: true,
      targetSetHash: challenge.targetSetHash,
      inputHash: challenge.inputHash,
      environmentNames: ["production"],
    };
    const outcome = await queue.enqueue({ ...args, productionConfirmation });
    expect(outcome?.sent).toBe(2);
    expect(write).toHaveBeenCalledTimes(2);
    for (const call of write.mock.calls) {
      expect(call[2]).toEqual({ targetTabIds: ["k-t1", "k-t2"], productionConfirmation });
    }
  });

  it("keeps input order when preflights complete at different times", async () => {
    const preflights: (() => void)[] = [];
    const prepare = vi.fn(() => new Promise<void>((resolve) => preflights.push(resolve)));
    const completed: Record<string, string[]> = { "k-t1": [], "k-t2": [] };
    const write: BroadcastWrite = (id, bytes) => {
      completed[id]?.push(new TextDecoder().decode(bytes));
      return Promise.resolve();
    };
    const queue = new BroadcastInputQueue(8);
    const args = { candidates, targetIds: ["t1", "t2"], write, prepare, revision: 1 };
    const first = queue.enqueue({ ...args, data: "a" });
    const second = queue.enqueue({ ...args, data: "b" });
    await flushTasks();
    expect(prepare).toHaveBeenCalledTimes(1);

    preflights[0]?.();
    await flushTasks();
    expect(prepare).toHaveBeenCalledTimes(2);
    preflights[1]?.();
    const outcomes = await Promise.all([first, second]);
    expect(outcomes.map((outcome) => outcome?.sent)).toEqual([2, 2]);
    expect(completed["k-t1"]).toEqual(["a", "b"]);
    expect(completed["k-t2"]).toEqual(["a", "b"]);
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
    expect(text).toContain("另有 2 个");
    expect(text).not.toContain("「d」");
  });
});

describe("broadcast store cleanup", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    useUi.setState({
      sessions: [session("s1", "ssh"), session("s2", "ssh")],
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

  it("prunes closed targets and turns off when the origin closes", async () => {
    enableBroadcast(["t1", "t2"]);
    await useUi.getState().closeTab("t2");
    expectBroadcast(["t1"]);
    await useUi.getState().closeTab("t1");
    expect(useUi.getState().broadcast).toBeNull();
  });

  it("keeps the broadcast set when an unrelated tab closes", async () => {
    enableBroadcast(["t1", "t2"]);
    await useUi.getState().closeTab("t3");
    expectBroadcast(["t1", "t2"]);
  });

  it("prunes targets closed in another workspace", async () => {
    useUi.setState({
      workspaces: [
        workspace([{ id: "p1", tabs: [termTab("t1", "s1")] }], "ws1"),
        workspace([{ id: "p2", tabs: [termTab("t4", "s2")] }], "ws2"),
      ],
    });
    enableBroadcast(["t1", "t4"]);
    await useUi.getState().closeTab("t4");
    expectBroadcast(["t1"]);
  });

  it("prunes dead targets removed with a workspace even when no close IPC runs", async () => {
    setTerminalCloseAction("detach");
    useUi.setState({
      workspaces: [
        workspace([{ id: "p1", tabs: [termTab("t1", "s1")] }], "ws1"),
        workspace([
          { id: "p4", tabs: [termTab("t4", "s2", { dead: true, tabId: undefined })] },
        ], "ws2"),
      ],
    });
    enableBroadcast(["t1", "t4"]);
    const generation = useUi.getState().broadcast?.generation ?? 0;
    await useUi.getState().closeWorkspace("ws2");
    expect(useUi.getState().workspaces.map((item) => item.id)).toEqual(["ws1"]);
    expectBroadcast(["t1"]);
    expect(useUi.getState().broadcast?.generation ?? 0).toBeGreaterThan(generation);
  });

  it("prunes targets removed by unsplit even when they need no close IPC", async () => {
    setTerminalCloseAction("detach");
    useUi.getState().updateTab("t3", { dead: true, tabId: undefined });
    enableBroadcast(["t1", "t3"]);
    const generation = useUi.getState().broadcast?.generation ?? 0;
    await useUi.getState().closePane("p2", "ws1");
    expect(useUi.getState().workspaces[0]?.panes.map((pane) => pane.id)).toEqual(["p1"]);
    expectBroadcast(["t1"]);
    expect(useUi.getState().broadcast?.generation ?? 0).toBeGreaterThan(generation);
  });

  it("clears the broadcast set when its origin workspace closes", async () => {
    setTerminalCloseAction("detach");
    enableBroadcast(["t1", "t2"]);
    await useUi.getState().closeWorkspace("ws1");
    expect(useUi.getState().broadcast).toBeNull();
  });

  it("prunes device targets and blocks queued writes when close fails", async () => {
    const deviceWorkspace = {
      ...workspace([{ id: "p4", tabs: [termTab("t4", "s2")] }], "ws-dev"),
      assetKind: "device",
      sessionId: "s2",
    };
    useUi.setState({
      workspaces: [
        workspace([{ id: "p1", tabs: [termTab("t1", "s1")] }], "ws1"),
        deviceWorkspace,
      ],
      activeWorkspaceId: "ws1",
    });
    enableBroadcast(["t1", "t4"]);
    const broadcast = useUi.getState().broadcast;
    expect(broadcast).not.toBeNull();
    if (!broadcast) return;
    useUi.getState().reportBroadcastTarget(broadcast.generation, {
      targetId: "t4",
      status: "pending",
      reason: null,
    });
    const gated = gatedBroadcastWrites();
    const write: BroadcastWrite = (id, bytes) =>
      id === "k-t4" ? gated.write(id, bytes) : Promise.resolve();
    const readCandidates = () => {
      const state = useUi.getState();
      return collectBroadcastCandidates(state.workspaces, state.sessions);
    };
    const queue = new BroadcastInputQueue(8);
    const args = {
      targetIds: broadcast.targetIds,
      write,
      revision: broadcast.generation,
      resolveTarget: (targetId: string) =>
        readCandidates().find((candidate) => candidate.storeTabId === targetId),
    };
    const first = queue.enqueue({ ...args, candidates: readCandidates(), data: "a" });
    const second = queue.enqueue({ ...args, candidates: readCandidates(), data: "b" });
    await flushTasks();
    expect(gated.calls).toEqual(["k-t4:a"]);

    vi.mocked(terminalApi.closeTab).mockRejectedValueOnce(new Error("offline"));
    const failures = await cleanupDeviceSessions();
    expect(failures).toMatchObject([{ operation: "kill", id: "t4" }]);
    expect(useUi.getState().workspaces.map((item) => item.id)).toEqual(["ws1"]);
    expectBroadcast(["t1"]);
    const pruned = useUi.getState().broadcast;
    expect(pruned?.generation ?? 0).toBeGreaterThan(broadcast.generation);
    expect(pruned?.results).toEqual({});

    gated.pending[0]?.();
    const [, secondOutcome] = await Promise.all([first, second]);
    expect(gated.calls).toEqual(["k-t4:a"]);
    expect(secondOutcome?.results.find((result) => result.targetId === "t4")).toMatchObject({
      status: "skipped",
      reason: "标签已关闭",
    });
  });

  it("drops queued and subsequent broadcast writes when device cleanup removes the origin", async () => {
    const originWorkspace = {
      ...workspace([{ id: "p1", tabs: [termTab("t1", "s1")] }], "ws1"),
      assetKind: "device",
      sessionId: "s1",
    };
    useUi.setState({
      workspaces: [
        originWorkspace,
        workspace([{ id: "p2", tabs: [termTab("t2", "s2")] }], "ws2"),
      ],
      activeWorkspaceId: "ws1",
    });
    enableBroadcast(["t1", "t2"]);
    const broadcast = useUi.getState().broadcast;
    expect(broadcast).not.toBeNull();
    if (!broadcast) return;
    useUi.getState().reportBroadcastTarget(broadcast.generation, {
      targetId: "t2",
      status: "pending",
      reason: null,
    });
    const gated = gatedBroadcastWrites();
    const readCandidates = () => {
      const state = useUi.getState();
      return collectBroadcastCandidates(state.workspaces, state.sessions);
    };
    const queue = new BroadcastInputQueue(8);
    const args = {
      targetIds: broadcast.targetIds,
      write: gated.write,
      revision: broadcast.generation,
      isCurrent: () => useUi.getState().broadcast?.generation === broadcast.generation,
      resolveTarget: (targetId: string) =>
        readCandidates().find((candidate) => candidate.storeTabId === targetId),
    };
    const first = queue.enqueue({ ...args, candidates: readCandidates(), data: "a" });
    const second = queue.enqueue({ ...args, candidates: readCandidates(), data: "b" });
    await flushTasks();
    expect(gated.calls).toEqual(["k-t1:a", "k-t2:a"]);

    vi.mocked(terminalApi.closeTab).mockRejectedValueOnce(new Error("offline"));
    const failures = await cleanupDeviceSessions();
    expect(failures).toMatchObject([{ operation: "kill", id: "t1" }]);
    expect(useUi.getState().workspaces.map((item) => item.id)).toEqual(["ws2"]);
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
    expect(useUi.getState().broadcast).toBeNull();

    for (const release of gated.pending) release();
    expect(await Promise.all([first, second])).toEqual([null, null]);
    expect(
      await queue.enqueue({ ...args, candidates: readCandidates(), data: "c" }),
    ).toBeNull();
    expect(gated.calls).toEqual(["k-t1:a", "k-t2:a"]);
  });
});
