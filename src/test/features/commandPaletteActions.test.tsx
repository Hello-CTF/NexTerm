/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitForRendered,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  snippetList: vi.fn(),
  create: vi.fn(),
  probeBatch: vi.fn(),
  connect: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  addTab: vi.fn(),
  setSessions: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      snippetList: mocks.snippetList,
      create: mocks.create,
      probeBatch: mocks.probeBatch,
    },
    sessionApi: { list: vi.fn().mockResolvedValue([]), connect: mocks.connect },
    dbApi: {},
    vaultApi: {},
    terminalApi: { write: mocks.write },
  };
});

import { JumpPalette } from "../../app/JumpPalette";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { Asset } from "../../ipc/commands";

function assetOf(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: null,
    kind: "ssh",
    host: "10.0.0.8",
    port: 22,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

const WEB = assetOf({ id: "a1", name: "web-01", credId: "cred-1" });
const BUILD = assetOf({
  id: "a2",
  name: "build-01",
  authKind: "key",
  keyPath: "/keys/ci_ed25519",
  host: "10.0.0.40",
  username: "ci",
});

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function optionByText(container: HTMLElement, text: string): HTMLButtonElement {
  const option = [...container.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(
    (o) => o.textContent?.includes(text),
  );
  if (!option) throw new Error(`Palette option not found: ${text}`);
  return option;
}

function hostOption(container: HTMLElement, name: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(
    (o) => o.querySelector("span span")?.textContent === name,
  );
}

function setTerminalWorkspace(tabId: string | null) {
  useUi.setState({
    workspaces: tabId
      ? [
          {
            id: "ws1",
            kind: "session" as const,
            title: "ws",
            closable: true,
            panes: [
              {
                id: "p1",
                activeTabId: "t1",
                tabs: [
                  {
                    id: "t1",
                    kind: "terminal" as const,
                    title: "Terminal 1",
                    tabId,
                    closable: true,
                  },
                ],
              },
            ],
            activePaneId: "p1",
          },
        ]
      : [],
    activeWorkspaceId: tabId ? "ws1" : null,
  });
}

function searchInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("Palette combobox not found");
  return input;
}

const realAddTab = useUi.getState().addTab;

function workspaceOf(id: string, kind: Workspace["kind"], sessionId?: string): Workspace {
  const paneId = `${id}-pane`;
  return {
    id,
    kind,
    title: id,
    sessionId,
    connId: kind === "db" ? "db-conn" : undefined,
    panes: [{ id: paneId, tabs: [], activeTabId: null }],
    activePaneId: paneId,
    closable: true,
  };
}

function allTabs(): AppTab[] {
  return useUi.getState().workspaces.flatMap((workspace) =>
    workspace.panes.flatMap((pane) => pane.tabs),
  );
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...BUILD }]);
  mocks.snippetList.mockResolvedValue([
    { id: "sn1", name: "disk usage", body: "df -h && du -sh /data/*", groupId: null, sort: 1 },
    { id: "sn2", name: "multi-line restart", body: "systemctl restart app\nsystemctl status app", groupId: null, sort: 2 },
  ]);
  mocks.ask.mockResolvedValue(true);
  mocks.create.mockImplementation((args: Partial<Asset> & { kind: string; name: string }) =>
    Promise.resolve(assetOf({ id: "a-new", ...args })),
  );
  mocks.probeBatch.mockResolvedValue({ results: [] });
  setTerminalWorkspace(null);
  useUi.setState({
    sessions: [],
    addTab: mocks.addTab,
    setSessions: mocks.setSessions,
    pushToast: mocks.toast,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
});

async function mountPalette(props: { onClose: () => void; onEditAsset?: (a: Asset) => void }) {
  mounted = mountWithClient(createElement(JumpPalette, props));
  await flush();
  await flush();
}

describe("command palette snippet entries", () => {
  it("lists snippets and inserts one into the active terminal", async () => {
    setTerminalWorkspace("kernel-1");
    const onClose = vi.fn();
    await mountPalette({ onClose });
    expect(optionByText(mounted!.container, "插入片段 disk usage")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "disk");
    click(optionByText(mounted!.container, "插入片段 disk usage"));
    await waitForRendered(() => expect(mocks.write).toHaveBeenCalled());
    expect(mocks.write).toHaveBeenCalledWith("kernel-1", new TextEncoder().encode("df -h && du -sh /data/*"));
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已插入「disk usage」"));
    expect(onClose).toHaveBeenCalled();
  });

  it("shows a toast and does not write to a terminal when no terminal is active", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "插入片段 disk usage")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "disk");
    click(optionByText(mounted!.container, "插入片段 disk usage"));
    await waitForRendered(() => expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("终端")));
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("requires safety confirmation for snippets with line breaks and does not write when cancelled", async () => {
    setTerminalWorkspace("kernel-1");
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "插入片段 multi-line restart")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "multi-line");
    mocks.ask.mockResolvedValueOnce(false);
    click(optionByText(mounted!.container, "插入片段 multi-line restart"));
    await waitForRendered(() => expect(mocks.ask).toHaveBeenCalled());
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("回车或换行"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("writes snippets with line breaks unchanged after confirmation", async () => {
    setTerminalWorkspace("kernel-1");
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "插入片段 multi-line restart")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "multi-line");
    mocks.ask.mockResolvedValueOnce(true);
    click(optionByText(mounted!.container, "插入片段 multi-line restart"));
    await waitForRendered(() =>
      expect(mocks.write).toHaveBeenCalledWith(
        "kernel-1",
        new TextEncoder().encode("systemctl restart app\nsystemctl status app"),
      ),
    );
  });
});

describe("command palette asset secondary actions", () => {
  it("provides a host row plus edit, clone, and hide entries for every visible asset", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(hostOption(mounted!.container, "web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "web-01");
    expect(hostOption(mounted!.container, "web-01")).toBeTruthy();
    for (const label of ["编辑资产 web-01", "克隆资产 web-01", "隐藏资产 web-01"]) {
      expect(optionByText(mounted!.container, label)).toBeTruthy();
    }
  });

  it("does not offer connections or probes for inventory-only assets", async () => {
    mocks.list.mockResolvedValue([
      { ...WEB },
      assetOf({ id: "docker-1", name: "docker-prod", kind: "docker-context", host: "ssh://ops@prod", options: { inventory: { source: "docker-context", key: "context/prod" } } }),
      assetOf({ id: "k8s-1", name: "ops/api-0", kind: "kubernetes", host: null, options: { inventory: { source: "kubernetes", key: "pod/current/ops/api-0" } } }),
    ]);
    await mountPalette({ onClose: vi.fn() });
    const options = [...mounted!.container.querySelectorAll('[role="option"]')].map((option) => option.textContent ?? "");
    expect(hostOption(mounted!.container, "web-01")).toBeTruthy();
    expect(hostOption(mounted!.container, "docker-prod")).toBeUndefined();
    expect(hostOption(mounted!.container, "ops/api-0")).toBeUndefined();
    expect(options.some((text) => text.includes("编辑资产 docker-prod"))).toBe(true);
    expect(options.some((text) => text.includes("编辑资产 ops/api-0"))).toBe(true);
    await waitForRendered(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    expect(mocks.probeBatch.mock.calls[0]![0]).toEqual(["a1"]);
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("does not offer a generic database action that opens the first asset and keeps per-asset connections", async () => {
    mocks.list.mockResolvedValue([
      assetOf({ id: "db", name: "db-01", kind: "postgres", host: "10.0.0.9", port: 5432 }),
    ]);
    await mountPalette({ onClose: vi.fn() });

    const options = [...mounted!.container.querySelectorAll('[role="option"]')];
    expect(options.some((option) => option.textContent?.includes("打开数据库工作台"))).toBe(false);
    expect(hostOption(mounted!.container, "db-01")).toBeTruthy();
  });

  it("passes the asset to the existing editor flow from the edit entry", async () => {
    const onEditAsset = vi.fn();
    await mountPalette({ onClose: vi.fn(), onEditAsset });
    expect(hostOption(mounted!.container, "web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "编辑资产 web-01");
    click(optionByText(mounted!.container, "编辑资产 web-01"));
    expect(onEditAsset).toHaveBeenCalledWith(expect.objectContaining({ id: "a1", name: "web-01" }));
  });

  it("cloning reuses the existing creation flow and shares credential references", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(hostOption(mounted!.container, "web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "克隆资产 web-01");
    click(optionByText(mounted!.container, "克隆资产 web-01"));
    await waitForRendered(() => expect(mocks.create).toHaveBeenCalled());
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({ name: "web-01 副本", credId: "cred-1", keyPath: null }),
    );
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("web-01 副本"));
  });

  it("hiding removes the asset from the palette without modifying data", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(hostOption(mounted!.container, "web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "隐藏资产 web-01");
    click(optionByText(mounted!.container, "隐藏资产 web-01"));
    await flush();
    expect(useAssetVisibility.getState().hiddenIds).toContain("a1");
    expect(mocks.create).not.toHaveBeenCalled();

    setInputValue(searchInput(mounted!.container), "web-01");
    await flush();
    expect(
      [...mounted!.container.querySelectorAll('[role="option"]')].some((o) =>
        o.textContent?.includes("web-01"),
      ),
    ).toBe(false);
  });

  it("showing hidden assets changes the entry to unhide", async () => {
    useAssetVisibility.getState().hide("a1");
    useAssetVisibility.getState().setShowHidden(true);
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "取消隐藏资产 web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "取消隐藏");
    click(optionByText(mounted!.container, "取消隐藏资产 web-01"));
    await flush();
    expect(useAssetVisibility.getState().hiddenIds).not.toContain("a1");
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("已取消隐藏"));
  });
});

describe("command palette workspace actions", () => {
  it("lists opened workspaces in a dedicated section and jumps to the selected workspace", async () => {
    const first = { ...workspaceOf("ws-a", "session", "s1"), title: "alpha-workspace" };
    const second = { ...workspaceOf("ws-b", "session", "s2"), title: "beta-workspace" };
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: first.id });
    const onClose = vi.fn();
    await mountPalette({ onClose });

    const firstOption = mounted!.container.querySelector('[role="option"]');
    expect(firstOption?.getAttribute("aria-selected")).toBe("true");
    expect(mounted!.container.querySelectorAll('[data-command-section="已打开的工作区"]')).toHaveLength(1);
    expect(optionByText(mounted!.container, "alpha-workspace").textContent).toContain("当前");
    setInputValue(searchInput(mounted!.container), "已打开的工作区");
    expect(mounted!.container.querySelectorAll('[role="option"]')).toHaveLength(2);

    click(optionByText(mounted!.container, "beta-workspace"));

    expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(mocks.addTab).not.toHaveBeenCalled();
  });

  it("opens port forwarding for the explicit current host", async () => {
    const first = workspaceOf("ws-a", "session", "s1");
    const second = workspaceOf("ws-b", "session", "s2");
    useUi.setState({
      workspaces: [first, second],
      activeWorkspaceId: second.id,
      addTab: realAddTab,
    });
    await mountPalette({ onClose: vi.fn() });

    click(optionByText(mounted!.container, "打开端口转发"));
    await flush();

    expect(allTabs().filter((tab) => tab.kind === "forward")).toEqual([
      expect.objectContaining({ sessionId: "s2" }),
    ]);
    expect(first.panes[0].tabs).toEqual([]);
  });

  it("keeps an explicit unscoped overview of all forwards", async () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null, addTab: realAddTab });
    await mountPalette({ onClose: vi.fn() });

    click(optionByText(mounted!.container, "查看全部端口转发"));
    await flush();

    const forwards = allTabs().filter((tab) => tab.kind === "forward");
    expect(forwards).toHaveLength(1);
    expect(forwards[0]).toMatchObject({ id: "forward-overview", kind: "forward" });
    expect(forwards[0]).not.toHaveProperty("sessionId");
  });

  it("opens background sessions with the current owning session", async () => {
    const first = workspaceOf("ws-a", "session", "s1");
    const second = workspaceOf("ws-b", "session", "s2");
    second.assetId = "asset-2";
    second.panes[0].tabs.push({ id: "durable", kind: "terminal", title: "terminal", sessionId: "s2", tabId: "kernel-durable", retainable: true, closable: true });
    useUi.setState({
      workspaces: [first, second],
      activeWorkspaceId: second.id,
      addTab: realAddTab,
    });
    await mountPalette({ onClose: vi.fn() });

    click(optionByText(mounted!.container, "打开后台会话"));
    await flush();

    expect(allTabs().filter((tab) => tab.kind === "background")).toEqual([
      expect.objectContaining({
        id: "background-ws-b",
        sessionId: "s2",
        assetId: "asset-2",
      }),
    ]);
    expect(useUi.getState().workspaces.some((workspace) => workspace.kind === "tools")).toBe(false);
  });

  it.each([{ kind: "tools" as const }, { kind: "db" as const }])(
    "host actions do not use an implicit session in a $kind workspace",
    async ({ kind }) => {
      const workspace = workspaceOf(`ws-${kind}`, kind);
      useUi.setState({
        workspaces: [workspace],
        activeWorkspaceId: workspace.id,
        addTab: realAddTab,
      });
      await mountPalette({ onClose: vi.fn() });

      click(optionByText(mounted!.container, "打开端口转发"));
      await flush();
      mounted?.unmount();
      mounted = undefined;
      await mountPalette({ onClose: vi.fn() });
      expect(mounted!.container.textContent).not.toContain("打开后台会话");

      expect(allTabs().filter((tab) => tab.kind === "forward" || tab.kind === "background")).toEqual([]);
      expect(mocks.toast).toHaveBeenCalledWith("info", "先连接一台主机（双击左侧资产）");
    },
  );

  it.each([false])("does not offer background sessions for confirmed direct terminals: %s", async (retainable) => {
    const workspace = workspaceOf("ws-direct", "session", "s1");
    workspace.panes[0].tabs.push({ id: "direct", kind: "terminal", title: "terminal", sessionId: "s1", tabId: "kernel-direct", retainable, closable: true });
    useUi.setState({ workspaces: [workspace], activeWorkspaceId: workspace.id });
    await mountPalette({ onClose: vi.fn() });

    expect(mounted!.container.textContent).not.toContain("打开后台会话");
  });

  it("opens ordinary history without an asset filter override", async () => {
    await mountPalette({ onClose: vi.fn() });

    click(optionByText(mounted!.container, "打开终端历史"));

    expect(mocks.addTab).toHaveBeenCalledWith({
      id: "history",
      kind: "history",
      title: "终端历史",
      closable: true,
    });
  });

  it("opens the batch execution tool", async () => {
    await mountPalette({ onClose: vi.fn() });

    click(optionByText(mounted!.container, "批量执行命令"));

    expect(mocks.addTab).toHaveBeenCalledWith({
      id: "exec-batch",
      kind: "execBatch",
      title: "批量执行",
      closable: true,
    });
  });

  it("opens the multi-host log tail as a dedicated log tab", async () => {
    await mountPalette({ onClose: vi.fn() });

    click(optionByText(mounted!.container, "多主机日志合并"));

    expect(mocks.addTab).toHaveBeenCalledWith({
      id: expect.stringMatching(/^multi-host-log-tail:/),
      kind: "log",
      title: "多主机日志",
      closable: true,
    });
  });

  it("opens one merged log tab in the tools workspace regardless of the active host workspace", async () => {
    const first = workspaceOf("ws-a", "session", "s1");
    const second = workspaceOf("ws-b", "session", "s2");
    useUi.setState({ workspaces: [first, second], activeWorkspaceId: first.id, addTab: realAddTab });
    await mountPalette({ onClose: vi.fn() });
    click(optionByText(mounted!.container, "多主机日志合并"));
    await flush();
    mounted?.unmount();
    mounted = undefined;

    useUi.setState({ activeWorkspaceId: second.id });
    await mountPalette({ onClose: vi.fn() });
    click(optionByText(mounted!.container, "多主机日志合并"));
    await flush();

    const { workspaces } = useUi.getState();
    const tools = workspaces.find((workspace) => workspace.kind === "tools");
    expect(tools?.panes.flatMap((pane) => pane.tabs).filter((tab) => tab.id.startsWith("multi-host-log-tail:"))).toHaveLength(1);
    for (const host of [first, second]) {
      expect(workspaces.find((workspace) => workspace.id === host.id)?.panes.flatMap((pane) => pane.tabs)).toHaveLength(0);
    }
  });
});

describe("command palette tab-to-new-pane actions", () => {
  function fileTab(id: string): AppTab {
    return { id, kind: "files", title: id, sessionId: "s1", path: `/${id}`, closable: true };
  }

  function setPaneTabs(tabs: AppTab[], activeTabId: string | null): void {
    const workspace = { ...workspaceOf("ws-a", "session", "s1"), panes: [{ id: "p1", tabs, activeTabId }], activePaneId: "p1" };
    useUi.setState({ workspaces: [workspace], activeWorkspaceId: workspace.id });
  }

  function panes() {
    return useUi.getState().workspaces[0].panes;
  }

  it("moves the current tab to a new pane on the right or below", async () => {
    setPaneTabs([fileTab("a"), fileTab("b"), fileTab("c")], "b");
    await mountPalette({ onClose: vi.fn() });
    click(optionByText(mounted!.container, "移到右侧新窗格"));
    expect(panes()).toHaveLength(2);
    const right = useUi.getState().workspaces[0].activePaneId;
    expect(panes().find((pane) => pane.id === right)?.tabs.map((tab) => tab.id)).toEqual(["b"]);
    expect(useUi.getState().workspaces[0].layoutTree).toMatchObject({ type: "split", dir: "row" });
    mounted?.unmount();

    useUi.getState().setActivePane("p1", "ws-a");
    await mountPalette({ onClose: vi.fn() });
    click(optionByText(mounted!.container, "移到下方新窗格"));
    expect(panes()).toHaveLength(3);
    expect(panes().find((pane) => pane.id === "p1")?.tabs).toHaveLength(1);
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("explains why nothing moved when the pane has one tab or none", async () => {
    setPaneTabs([fileTab("a")], "a");
    await mountPalette({ onClose: vi.fn() });
    click(optionByText(mounted!.container, "移到右侧新窗格"));
    expect(panes()).toHaveLength(1);
    expect(mocks.toast).toHaveBeenLastCalledWith("info", expect.stringContaining("只有这一个标签"));
    mounted?.unmount();

    setPaneTabs([], null);
    await mountPalette({ onClose: vi.fn() });
    click(optionByText(mounted!.container, "移到下方新窗格"));
    expect(mocks.toast).toHaveBeenLastCalledWith("info", "当前窗格没有标签");
  });
});
