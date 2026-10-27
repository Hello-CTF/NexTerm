/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  listSessions: vi.fn(),
  closeTab: vi.fn(),
  resyncSessions: vi.fn(),
  helperStatus: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: mocks.askChoiceRemember }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { list: mocks.listSessions },
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable, closeTab: mocks.closeTab },
    localHelperApi: { status: mocks.helperStatus },
  });
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { collectBackgroundTabs } from "../../features/terminal/backgroundSessionsAccess";
import { useAuth } from "../../features/auth/store";
import { setLocalHelperStatusForTest } from "../../features/terminal/localHelperStatus";
import { useUi, type Workspace } from "../../app/store";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";
import type { DurableDescriptor, LiveTabInfo, SessionAttentionState, SessionInfo } from "../../ipc/commands";

const NOW = new Date(2026, 9, 27, 12, 0, 0).getTime();
const MINUTE = 60_000;

function liveTab(extra: Partial<LiveTabInfo> & { tabId: string }): LiveTabInfo {
  return {
    sessionId: "s1",
    sessionName: "主机一",
    sessionKind: "local",
    cols: 80,
    rows: 24,
    controller: null,
    subscribers: 0,
    viewers: 0,
    exited: false,
    lastOutputMsAgo: 1000,
    ...extra,
  };
}

function descriptor(extra: Partial<DurableDescriptor> & { id: string }): DurableDescriptor {
  return {
    incarnation: `inc-${extra.id}`,
    createdAt: NOW - 60 * MINUTE,
    metadataRevision: 0,
    cols: 120,
    rows: 40,
    dead: false,
    exitCode: null,
    finishedAt: null,
    ...extra,
  };
}

const s1: SessionInfo = { id: "s1", assetId: "asset-one", name: "主机一", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
const s2: SessionInfo = { id: "s2", assetId: "asset-two", name: "主机二", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };

function hostWorkspace(id: string, session: SessionInfo, assetKind?: "ssh" | "local"): Workspace {
  return {
    id,
    kind: "session",
    title: session.name,
    sessionId: session.id,
    ...(assetKind === "ssh" ? { assetId: session.assetId ?? undefined, assetKind } : assetKind ? { assetKind } : {}),
    panes: [{ id: `${id}-pane`, tabs: [], activeTabId: null }],
    activePaneId: `${id}-pane`,
    closable: true,
  };
}

function seedHost(session: SessionInfo, assetKind?: "ssh" | "local") {
  useUi.setState({
    workspaces: [hostWorkspace("ws-host", session, assetKind)],
    activeWorkspaceId: "ws-host",
    sessions: [session],
    toasts: [],
    resyncSessions: mocks.resyncSessions,
  });
}

function seedAllHosts() {
  useUi.setState({
    workspaces: [
      { id: "tools", kind: "tools", title: "工具", panes: [{ id: "p", tabs: [], activeTabId: null }], activePaneId: "p", closable: true },
      hostWorkspace("ws-one", s1, "ssh"),
      hostWorkspace("ws-two", s2, "ssh"),
    ],
    activeWorkspaceId: "tools",
    sessions: [s1, s2],
    toasts: [],
    resyncSessions: mocks.resyncSessions,
  });
}

function rows(view: MountedView): HTMLTableRowElement[] {
  return [...view.container.querySelectorAll<HTMLTableRowElement>("tbody tr[data-background-row]")];
}

function row(view: MountedView, tabId: string): HTMLTableRowElement {
  const found = view.container.querySelector<HTMLTableRowElement>(`tbody tr[data-background-row="${tabId}"]`);
  if (!found) throw new Error(`row ${tabId} not found`);
  return found;
}

function cell(target: ParentNode, testId: string): HTMLElement {
  const found = target.querySelector<HTMLElement>(`[data-testid="${testId}"]`);
  if (!found) throw new Error(`${testId} not found`);
  return found;
}

function button(target: ParentNode, label: string): HTMLButtonElement {
  const found = [...target.querySelectorAll<HTMLButtonElement>("button")].find((candidate) => candidate.textContent?.trim() === label);
  if (!found) throw new Error(`${label} button not found`);
  return found;
}

async function mountLoaded(props: Record<string, unknown>, ready: (view: MountedView) => void): Promise<MountedView> {
  const view = mount(createElement(BackgroundSessions, props));
  await flush();
  await waitFor(() => ready(view));
  return view;
}

describe("BackgroundSessions table", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.spyOn(Date, "now").mockReturnValue(NOW);
    window.localStorage.clear();
    useAuth.setState({ user: null });
    mocks.ask.mockResolvedValue(true);
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listSessions.mockResolvedValue([s1, s2]);
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.resyncSessions.mockResolvedValue(undefined);
    mocks.helperStatus.mockResolvedValue({ state: "disabled" });
    setLocalHelperStatusForTest(null);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    setLocalHelperStatusForTest(null);
  });

  it("shows the 名称 / 状态 / 大小 / 时间 / 操作 columns without a host column", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "t1", title: "构建" })]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      const headers = [...view.container.querySelectorAll("thead th")].map((th) => th.textContent?.trim());
      expect(headers).toEqual(["名称", "状态", "大小", "时间", "操作"]);
      expect(row(view, "t1").cells).toHaveLength(5);
      expect(view.container.querySelector('[data-testid="background-host-name"]')).toBeNull();
      expect(view.container.querySelector('[data-testid="background-host"]')?.textContent).toBe("主机一");
      // A regular terminal has nothing to explain.
      expect(view.container.querySelector('[data-testid="background-old-helper-note"]')).toBeNull();
      expect(view.container.querySelector('[data-testid="background-needs-me-note"]')).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("puts the host name under the terminal name only in the all-hosts view", async () => {
    seedAllHosts();
    mocks.listDurable.mockImplementation(async (sessionId: string) =>
      sessionId === "s1" ? [descriptor({ id: "one", title: "一号任务" })] : [descriptor({ id: "two", title: "二号任务" })]);
    const view = await mountLoaded({}, (v) => expect(rows(v)).toHaveLength(2));
    try {
      expect(cell(row(view, "one"), "background-host-name").textContent).toBe("主机一");
      expect(cell(row(view, "two"), "background-host-name").textContent).toBe("主机二");
      expect(cell(row(view, "one"), "background-name").textContent).toBe("一号任务");
    } finally {
      view.unmount();
    }
  });

  it("names a terminal by its custom or program title and falls back to a short id, never its size", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    const long = `${"很长的终端名称".repeat(12)}👩‍💻`;
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "custom", title: "部署脚本", titleCustom: true }),
      liveTab({ tabId: "program", title: "vim\u0007 notes.md" }),
      liveTab({ tabId: "0198a3f2-7c41-7000-8000-00000abc1234" }),
      liveTab({ tabId: "long", title: long, titleCustom: true }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(4));
    try {
      expect(cell(row(view, "custom"), "background-name").textContent).toBe("部署脚本");
      expect(cell(row(view, "program"), "background-name").textContent).toBe("vim notes.md");
      const fallback = cell(row(view, "0198a3f2-7c41-7000-8000-00000abc1234"), "background-name");
      expect(fallback.textContent).toBe("终端 bc1234");
      expect(fallback.textContent).not.toContain("×");
      const longName = cell(row(view, "long"), "background-name");
      expect(longName.classList.contains("truncate")).toBe(true);
      expect(longName.title).toBe(longName.textContent);
      // The name is clipped to whole graphemes: the emoji sequence is either kept whole or dropped.
      const shown = longName.textContent ?? "";
      expect(shown.endsWith("‍")).toBe(false);
      expect(/[\uD800-\uDBFF]$/.test(shown)).toBe(false);
    } finally {
      view.unmount();
    }
  });

  it("labels each helper state and only accents needs_me", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    const states: [SessionAttentionState, string][] = [
      ["running", "运行中"],
      ["needs_me", "需要我"],
      ["just_finished", "刚完成"],
      ["idle", "空闲"],
      ["exited", "已退出"],
    ];
    mocks.listLive.mockResolvedValue([
      ...states.map(([state]) => liveTab({ tabId: state, title: state, durable: true, state })),
      liveTab({ tabId: "regular", title: "普通" }),
      liveTab({ tabId: "future", title: "新状态", durable: true, state: "thinking" as SessionAttentionState }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(7));
    try {
      for (const [state, label] of states) {
        const status = cell(row(view, state), "background-status");
        expect(status.textContent).toBe(label);
        expect(status.classList.contains("text-blue-300")).toBe(state === "needs_me");
        expect(status.classList.contains("text-[var(--nx-fg-soft)]")).toBe(state === "exited");
      }
      const regular = cell(row(view, "regular"), "background-status");
      expect(regular.textContent).toBe("—");
      expect(regular.title).toContain("普通终端不会报告状态");
      const future = cell(row(view, "future"), "background-status");
      expect(future.textContent).toBe("—");
      expect(future.title).toBe("未知状态");
      // A newer state is not mistaken for an old component.
      expect(view.container.querySelector('[data-testid="background-old-helper-note"]')).toBeNull();
      expect(view.container.querySelector('[class*="amber"]')).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("shows size in mono and the output and detach times on two lines", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "busy", title: "构建", cols: 132, rows: 43, lastOutputAt: NOW - 3 * MINUTE, lastOutputMsAgo: 3 * MINUTE,
        detachedAt: NOW - 20 * MINUTE, detachedMsAgo: 20 * MINUTE, createdAt: NOW - 90 * MINUTE }),
      liveTab({ tabId: "quiet", title: "安静", lastOutputAt: 0, lastOutputMsAgo: 0, detachedAt: NOW - 2 * MINUTE, detachedMsAgo: 2 * MINUTE }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(2));
    try {
      const busy = row(view, "busy");
      const size = cell(busy, "background-size");
      expect(size.textContent).toBe("132×43");
      expect(size.classList.contains("font-mono")).toBe(true);
      expect(cell(busy, "background-output").textContent).toBe("3 分钟前有输出");
      expect(cell(busy, "background-detached").textContent).toBe("20 分钟前转入后台");
      const tooltip = cell(busy, "background-time").title;
      expect(tooltip).toContain("最近输出：2026-10-27 11:57:00");
      expect(tooltip).toContain("转入后台：2026-10-27 11:40:00");
      expect(tooltip).toContain("创建：2026-10-27 10:30:00");
      expect(cell(row(view, "quiet"), "background-output").textContent).toBe("暂无输出");
      expect(cell(row(view, "quiet"), "background-detached").textContent).toBe("2 分钟前转入后台");
    } finally {
      view.unmount();
    }
  });

  it("reads remote durable status and times from the helper and marks an old helper's times unknown", async () => {
    seedHost(s1, "ssh");
    mocks.listDurable.mockResolvedValue([
      descriptor({ id: "current", title: "训练", state: "needs_me",
        activity: { lastOutputAt: NOW - 5 * MINUTE, detachedAt: NOW - 30 * MINUTE, attached: false } }),
      descriptor({ id: "silent", title: "静默", state: "idle", activity: { lastOutputAt: null, detachedAt: null, attached: true } }),
      descriptor({ id: "old", title: "旧组件" }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(3));
    try {
      const current = row(view, "current");
      expect(cell(current, "background-status").textContent).toBe("需要我");
      expect(cell(current, "background-size").textContent).toBe("120×40");
      expect(cell(current, "background-output").textContent).toBe("5 分钟前有输出");
      expect(cell(current, "background-detached").textContent).toBe("30 分钟前转入后台");
      expect(cell(current, "background-time").title).toContain("创建（主机时间）：2026-10-27 11:00:00");
      // A helper that reports times without ages: its times are labelled as host time.
      expect(cell(current, "background-time").title).toContain("最近输出（主机时间）：2026-10-27 11:55:00");

      const silent = row(view, "silent");
      expect(cell(silent, "background-output").textContent).toBe("暂无输出");
      expect(cell(silent, "background-detached").textContent).toBe("仍有设备连接着");

      const old = row(view, "old");
      expect(cell(old, "background-status").textContent).toBe("—");
      expect(cell(old, "background-status").title).toContain("版本较旧");
      expect(cell(old, "background-output").textContent).toBe("—");
      expect(cell(old, "background-detached").textContent).toBe("—");
      expect(cell(old, "background-time").title).toContain("版本较旧");
      expect(cell(old, "background-time").title).toContain("创建（主机时间）：2026-10-27 11:00:00");
      expect(view.container.textContent).not.toContain("在主机上运行");
      // Touch screens get no tooltips: the old component and 需要我 are explained in visible text.
      expect(cell(view.container, "background-old-helper-note").textContent).toBe(
        "部分终端所在主机的持久组件版本较旧，无法显示状态和时间；更新组件后即可显示");
      expect(cell(view.container, "background-needs-me-note").textContent).toContain("「需要我」表示程序在等你回应");
    } finally {
      view.unmount();
    }
  });

  it("reads local durable terminals' status and times from the local helper", async () => {
    mocks.helperStatus.mockResolvedValue({ state: "ready" });
    setLocalHelperStatusForTest({ state: "ready" });
    const local = { ...s1, kind: "local", assetId: "local-asset", name: "本机" };
    seedHost(local, "local");
    mocks.listSessions.mockResolvedValue([local]);
    mocks.listDurable.mockResolvedValue([
      descriptor({ id: "local-shell", title: "本机构建", state: "running",
        activity: { lastOutputAt: NOW - 10_000, detachedAt: NOW - 8 * MINUTE, attached: false } }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      expect(mocks.listDurable).toHaveBeenCalledWith("s1");
      const shell = row(view, "local-shell");
      expect(cell(shell, "background-name").textContent).toBe("本机构建");
      expect(cell(shell, "background-status").textContent).toBe("运行中");
      expect(cell(shell, "background-output").textContent).toBe("10 秒前有输出");
      expect(cell(shell, "background-detached").textContent).toBe("8 分钟前转入后台");
    } finally {
      view.unmount();
    }
  });

  it("keeps the old-component note away from a current helper that reports no state", async () => {
    seedHost(s1, "ssh");
    mocks.listDurable.mockResolvedValue([
      descriptor({ id: "stateless", title: "无状态", activity: { lastOutputAt: NOW - MINUTE, detachedAt: NOW - 2 * MINUTE, attached: false,
        asOf: NOW, lastOutputMsAgo: MINUTE, detachedMsAgo: 2 * MINUTE } }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      expect(cell(view.container, "background-status").textContent).toBe("—");
      expect(cell(view.container, "background-detached").textContent).toBe("2 分钟前转入后台");
      expect(view.container.querySelector('[data-testid="background-old-helper-note"]')).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("counts each listing's ages from when that listing arrived", () => {
    const local = { ...s1, id: "local", kind: "local", assetId: null };
    const collected = collectBackgroundTabs(
      { sessions: [local, s2], workspaces: [], verified: {} },
      {
        live: [liveTab({ tabId: "live", sessionId: "local" })],
        liveFetchedAt: NOW - 4000,
        remote: [{ session: s2, tabs: [descriptor({ id: "slow" })], fetchedAt: NOW - 1000 }],
        openTabIds: new Set(),
      },
    );
    const fetched = Object.fromEntries(collected.list.map((tab) => [tab.tabId, tab.fetchedAt]));
    expect(fetched).toEqual({ live: NOW - 4000, slow: NOW - 1000 });
  });

  it("shows helper ages instead of comparing this device's clock with a skewed host clock", async () => {
    seedHost(s1, "ssh");
    const hostNow = NOW + 60 * MINUTE;
    mocks.listDurable.mockResolvedValue([
      descriptor({ id: "skewed", title: "偏差", state: "idle", activity: {
        lastOutputAt: hostNow - 5 * MINUTE, detachedAt: hostNow - 30 * MINUTE, attached: false,
        asOf: hostNow, lastOutputMsAgo: 5 * MINUTE, detachedMsAgo: 30 * MINUTE,
      } }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      expect(cell(view.container, "background-output").textContent).toBe("5 分钟前有输出");
      expect(cell(view.container, "background-detached").textContent).toBe("30 分钟前转入后台");
      const tooltip = cell(view.container, "background-time").title;
      // Rebased to this device's clock, so not labelled as host time.
      expect(tooltip).toContain("最近输出：2026-10-27 11:55:00");
      expect(tooltip).toContain("转入后台：2026-10-27 11:30:00");
      expect(tooltip).not.toContain("最近输出（主机时间）");
    } finally {
      view.unmount();
    }
  });

  it("reads an omitted live detach age as zero, not as a clock comparison", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "fresh", title: "刚放", detachedAt: NOW + 10 * MINUTE })]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      expect(cell(view.container, "background-detached").textContent).toBe("刚刚转入后台");
    } finally {
      view.unmount();
    }
  });

  it("uses the backend's detach age for live rows", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    // The backend clock runs ten minutes ahead of this device.
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "ahead", title: "超前", detachedAt: NOW + 10 * MINUTE - 3 * MINUTE, detachedMsAgo: 3 * MINUTE }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      expect(cell(view.container, "background-detached").textContent).toBe("3 分钟前转入后台");
      expect(cell(view.container, "background-time").title).toContain("转入后台：2026-10-27 11:57:00");
    } finally {
      view.unmount();
    }
  });

  it("adds the helper's state and creation time to a durable tab this app still holds", async () => {
    seedHost(s1, "ssh");
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "held", sessionKind: "ssh", title: "持有", durable: true, detachedAt: NOW - 4 * MINUTE, detachedMsAgo: 4 * MINUTE, createdAt: NOW - MINUTE }),
    ]);
    mocks.listDurable.mockResolvedValue([
      descriptor({ id: "held", title: "持有", state: "just_finished", createdAt: NOW - 120 * MINUTE,
        activity: { lastOutputAt: NOW - MINUTE, detachedAt: null, attached: true } }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(cell(v.container, "background-status").textContent).toBe("刚完成"));
    try {
      expect(rows(view)).toHaveLength(1);
      expect(cell(view.container, "background-detached").textContent).toBe("4 分钟前转入后台");
      expect(cell(view.container, "background-time").title).toContain("创建（主机时间）：2026-10-27 10:00:00");
    } finally {
      view.unmount();
    }
  });

  it("marks a detach time that only starts when this app reopened an older durable tab", async () => {
    seedHost(s1, "ssh");
    const opened = NOW - 6 * MINUTE;
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "reopened", sessionKind: "ssh", title: "重开", durable: true, createdAt: opened, detachedAt: opened, detachedMsAgo: 6 * MINUTE }),
    ]);
    mocks.listDurable.mockResolvedValue([
      descriptor({ id: "reopened", title: "重开", state: "idle", createdAt: NOW - 300 * MINUTE,
        activity: { lastOutputAt: null, detachedAt: null, attached: true, asOf: NOW } }),
    ]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(cell(v.container, "background-status").textContent).toBe("空闲"));
    try {
      expect(cell(view.container, "background-detached").textContent).toBe("至少 6 分钟前转入后台");
      expect(cell(view.container, "background-time").title).toContain("转入后台：不晚于 2026-10-27 11:54:00（自本次打开应用起）");
    } finally {
      view.unmount();
    }
  });

  it("keeps takeover and kill with their confirmation", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    mocks.listSessions.mockResolvedValue([{ ...s1, kind: "local", assetId: null }]);
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "take" }), liveTab({ tabId: "kill" })]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(2));
    try {
      for (const current of rows(view)) {
        expect(button(current, "接管").title).toBe("回到这个终端，并补上离开期间的输出");
        expect(button(current, "结束进程").title).toBe("结束这个终端里运行的程序，无法恢复");
      }
      await act(async () => {
        button(row(view, "kill"), "结束进程").click();
        await flush();
      });
      expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("无法恢复"), { title: "结束进程", kind: "warning" });
      expect(mocks.closeTab).toHaveBeenCalledWith("kill", "kill", "s1");
      await act(async () => {
        button(row(view, "take"), "接管").click();
        await flush();
      });
      const opened = useUi.getState().workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs));
      expect(opened.some((tab) => tab.kind === "terminal" && tab.tabId === "take")).toBe(true);
      expect(view.container.querySelector('tr[data-background-row="take"]')).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("lays rows out as cards with name and status first and actions last", async () => {
    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "card", title: "卡片" })]);
    const view = await mountLoaded({ workspaceId: "ws-host" }, (v) => expect(rows(v)).toHaveLength(1));
    try {
      const table = cell(view.container, "background-table");
      expect(table.classList.contains("nx-table-cards")).toBe(true);
      expect(table.classList.contains("nx-bg-sessions")).toBe(true);
      const cells = [...row(view, "card").cells];
      expect(cells.map((td) => td.className.split(" ").find((name) => name.startsWith("nx-")))).toEqual([
        "nx-bg-name", "nx-bg-status", "nx-bg-size", "nx-bg-time", "nx-right",
      ]);
      expect(cells[2].dataset.label).toBe("大小");
    } finally {
      view.unmount();
    }
  });

  it("fits pending, empty and loading rows to the five columns", async () => {
    useUi.setState({
      workspaces: [{ ...hostWorkspace("ws-host", s1, "ssh"), panes: [{ id: "p", tabs: [
        { id: "pending", kind: "terminal", title: "未核实的任务", sessionId: "s1", tabId: "kernel-pending", closable: true, restorePending: true },
      ], activeTabId: "pending" }], activePaneId: "p" }],
      activeWorkspaceId: "ws-host",
      sessions: [{ ...s1, status: "disconnected" }],
      toasts: [],
      resyncSessions: mocks.resyncSessions,
    });
    const pending = mount(createElement(BackgroundSessions, { workspaceId: "ws-host" }));
    try {
      await flush();
      const pendingRow = pending.container.querySelector<HTMLTableRowElement>("tr[data-background-pending]");
      expect(pendingRow?.cells).toHaveLength(5);
      expect(cell(pendingRow as HTMLElement, "background-name").textContent).toBe("未核实的任务");
      expect(cell(pendingRow as HTMLElement, "background-status").textContent).toBe("待核实");
      expect(pendingRow?.textContent).toContain("连接主机后才能确认是否仍在运行");
      expect(button(pendingRow as HTMLElement, "连接并查找")).toBeTruthy();
    } finally {
      pending.unmount();
    }

    seedHost({ ...s1, kind: "local", assetId: null }, "local");
    let release: (value: LiveTabInfo[]) => void = () => {};
    mocks.listLive.mockReturnValue(new Promise<LiveTabInfo[]>((resolve) => { release = resolve; }));
    const loading = mount(createElement(BackgroundSessions, { workspaceId: "ws-host" }));
    try {
      await flush();
      const skeletons = loading.container.querySelectorAll<HTMLTableRowElement>('tbody tr[aria-hidden="true"]');
      expect(skeletons.length).toBeGreaterThan(0);
      for (const skeleton of skeletons) expect(skeleton.cells).toHaveLength(5);
      await act(async () => {
        release([]);
        await flush();
      });
      await waitFor(() => expect(loading.container.querySelector(".nx-table-empty")).not.toBeNull());
      const empty = loading.container.querySelector<HTMLTableCellElement>(".nx-table-empty");
      expect(empty?.colSpan).toBe(5);
      expect(empty?.classList.contains("text-[var(--nx-fg-soft)]")).toBe(true);
    } finally {
      loading.unmount();
    }
  });
});
