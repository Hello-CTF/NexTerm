/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  snippetList: vi.fn(),
  create: vi.fn(),
  probeBatch: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
  addTab: vi.fn(),
  setSessions: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
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
    sessionApi: {},
    dbApi: {},
    vaultApi: {},
    terminalApi: { write: mocks.write },
  };
});

import { CommandPalette } from "../../app/CommandPalette";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useUi } from "../../app/store";
import type { LayoutPreset } from "../../app/layoutPresets";
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
                    title: "终端 1",
                    tabId,
                    closable: true,
                  },
                ],
              },
            ],
            activePaneId: "p1",
            splitRatio: 0.5,
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

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...BUILD }]);
  mocks.snippetList.mockResolvedValue([
    { id: "sn1", name: "磁盘水位", body: "df -h && du -sh /data/*", groupId: null, sort: 1 },
    { id: "sn2", name: "多行重启", body: "systemctl restart app\nsystemctl status app", groupId: null, sort: 2 },
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
    layoutPresets: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
});

async function mountPalette(props: { onClose: () => void; onEditAsset?: (a: Asset) => void }) {
  mounted = mountWithClient(createElement(CommandPalette, props));
  await flush();
  await flush();
}

describe("命令面板片段条目", () => {
  it("列出片段并插入到当前终端", async () => {
    setTerminalWorkspace("kernel-1");
    const onClose = vi.fn();
    await mountPalette({ onClose });
    expect(optionByText(mounted!.container, "插入片段 磁盘水位")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "磁盘");
    click(optionByText(mounted!.container, "插入片段 磁盘水位"));
    await waitFor(() => expect(mocks.write).toHaveBeenCalled());
    expect(mocks.write).toHaveBeenCalledWith("kernel-1", new TextEncoder().encode("df -h && du -sh /data/*"));
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已插入「磁盘水位」"));
    expect(onClose).toHaveBeenCalled();
  });

  it("没有活动终端时提示且不写终端", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "插入片段 磁盘水位")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "磁盘");
    click(optionByText(mounted!.container, "插入片段 磁盘水位"));
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("终端")));
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("含回车的片段先经安全确认，取消则不写入", async () => {
    setTerminalWorkspace("kernel-1");
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "插入片段 多行重启")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "多行");
    mocks.ask.mockResolvedValueOnce(false);
    click(optionByText(mounted!.container, "插入片段 多行重启"));
    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("回车/换行"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("含回车的片段确认后按原样写入", async () => {
    setTerminalWorkspace("kernel-1");
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "插入片段 多行重启")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "多行");
    mocks.ask.mockResolvedValueOnce(true);
    click(optionByText(mounted!.container, "插入片段 多行重启"));
    await waitFor(() =>
      expect(mocks.write).toHaveBeenCalledWith(
        "kernel-1",
        new TextEncoder().encode("systemctl restart app\nsystemctl status app"),
      ),
    );
  });
});

describe("命令面板资产二级操作", () => {
  it("每个可见资产都有连接/编辑/克隆/隐藏条目", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "连接 web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "web-01");
    for (const label of ["连接 web-01", "编辑资产 web-01", "克隆资产 web-01", "隐藏资产 web-01"]) {
      expect(optionByText(mounted!.container, label)).toBeTruthy();
    }
  });

  it("编辑条目把资产交给现有编辑器流程", async () => {
    const onEditAsset = vi.fn();
    await mountPalette({ onClose: vi.fn(), onEditAsset });
    expect(optionByText(mounted!.container, "连接 web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "编辑资产 web-01");
    click(optionByText(mounted!.container, "编辑资产 web-01"));
    expect(onEditAsset).toHaveBeenCalledWith(expect.objectContaining({ id: "a1", name: "web-01" }));
  });

  it("克隆复用现有创建路径并共享凭据引用", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "连接 web-01")).toBeTruthy();

    setInputValue(searchInput(mounted!.container), "克隆资产 web-01");
    click(optionByText(mounted!.container, "克隆资产 web-01"));
    await waitFor(() => expect(mocks.create).toHaveBeenCalled());
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({ name: "web-01 副本", credId: "cred-1", keyPath: null }),
    );
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("web-01 副本"));
  });

  it("隐藏后该资产从面板消失且不触碰数据", async () => {
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "连接 web-01")).toBeTruthy();

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

  it("显示隐藏时条目变为取消隐藏", async () => {
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

function presetFixture(): LayoutPreset {
  return {
    id: "p1",
    name: "开发环境",
    createdAt: 1,
    updatedAt: 1,
    workspace: {
      kind: "session",
      title: "web-01",
      sessionId: "s1",
      assetId: "a1",
      assetKind: "ssh",
      splitRatio: 0.5,
      panes: [
        { tabs: [{ kind: "terminal", title: "终端 1", sessionId: "s1" }], activeTabIndex: 0 },
      ],
      activePaneIndex: 0,
    },
  };
}

describe("命令面板布局预设", () => {
  it("列出保存/应用/删除预设条目", async () => {
    useUi.setState({ layoutPresets: [presetFixture()] });
    await mountPalette({ onClose: vi.fn() });
    expect(optionByText(mounted!.container, "保存布局预设")).toBeTruthy();
    expect(optionByText(mounted!.container, "应用布局预设「开发环境」")).toBeTruthy();
    expect(optionByText(mounted!.container, "删除布局预设「开发环境」")).toBeTruthy();
  });

  it("保存布局预设走命名提示并写入 store", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.promptText.mockResolvedValue("我的预设");
    await mountPalette({ onClose: vi.fn() });
    setInputValue(searchInput(mounted!.container), "保存布局预设");
    click(optionByText(mounted!.container, "保存布局预设"));
    await waitFor(() => expect(useUi.getState().layoutPresets).toHaveLength(1));
    expect(mocks.promptText).toHaveBeenCalledWith("预设名称：", "ws");
    expect(useUi.getState().layoutPresets[0].name).toBe("我的预设");
    expect(useUi.getState().layoutPresets[0].workspace.panes[0].tabs[0].kind).toBe("terminal");
  });

  it("删除布局预设需确认", async () => {
    useUi.setState({ layoutPresets: [presetFixture()] });
    mocks.ask.mockResolvedValueOnce(true);
    await mountPalette({ onClose: vi.fn() });
    setInputValue(searchInput(mounted!.container), "删除布局预设");
    click(optionByText(mounted!.container, "删除布局预设「开发环境」"));
    await waitFor(() => expect(useUi.getState().layoutPresets).toHaveLength(0));
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("删除预设「开发环境」"),
      expect.anything(),
    );
  });
});
