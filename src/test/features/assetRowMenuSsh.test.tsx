/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    list: vi.fn(),
    search: vi.fn(),
    groupList: vi.fn(),
    groupUpdate: vi.fn(),
    groupDelete: vi.fn(),
    snippetList: vi.fn(),
    listCredentials: vi.fn(),
    probe: vi.fn(),
    connectQuick: vi.fn(),
    ask: vi.fn(),
    promptText: vi.fn(),
    writeText: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});
vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return {
    ...actual,
    browserFilesAvailable: () => false,
    pickBrowserFile: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      search: mocks.search,
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      groupList: mocks.groupList,
      groupUpdate: mocks.groupUpdate,
      groupDelete: mocks.groupDelete,
      snippetList: mocks.snippetList,
      readKeyFile: vi.fn(),
    },
    vaultApi: { listCredentials: mocks.listCredentials },
    sessionApi: { probe: mocks.probe, connect: vi.fn(), connectQuick: mocks.connectQuick, list: vi.fn() },
    terminalApi: {},
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useUi } from "../../app/store";
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

const WEB = assetOf({ id: "a1", name: "web-1" });
const EDGE = assetOf({ id: "a2", name: "edge-1", host: "edge.example.com", port: 2222, username: "deploy" });
const WINRM = assetOf({ id: "a3", name: "win-1", kind: "winrm", host: "10.0.0.9", port: 5985 });

const QUICK_SESSION = {
  id: "s-quick",
  assetId: null,
  name: "ops@10.0.0.8",
  kind: "ssh",
  status: "connected" as const,
  tabs: [],
  createdAt: 0,
};

let mounted: MountedView | undefined;

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function rowByText(container: HTMLElement, text: string): HTMLElement {
  const row = [...container.querySelectorAll<HTMLElement>('[role="treeitem"][title]')].find((r) =>
    (r.getAttribute("title") ?? "").startsWith(text),
  );
  if (!row) throw new Error(`Asset row not found: ${text}`);
  return row;
}

function openRowMenu(container: HTMLElement, name: string): void {
  click(
    [...container.querySelectorAll("button")].find(
      (b) => b.getAttribute("aria-label") === `更多操作 ${name}`,
    ) ??
      (() => {
        throw new Error(`More button not found: ${name}`);
      })(),
  );
}

function menuLabels(container: ParentNode): string[] {
  return [...container.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent ?? "");
}

function clickMenuItem(container: ParentNode, label: string): void {
  const item = [...container.querySelectorAll('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
  if (!item) throw new Error(`Menu item not found: ${label}`);
  click(item);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: mocks.writeText } });
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...EDGE }, { ...WINRM }]);
  mocks.search.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.writeText.mockResolvedValue(undefined);
  mocks.connectQuick.mockResolvedValue(QUICK_SESSION);
  useUi.setState({
    leftOpen: true,
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, connectingAssetIds: [] });
});

describe("复制 SSH 命令", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("默认端口复制为 ssh user@host 并提示", async () => {
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "复制 SSH 命令");
    await waitFor(() =>
      expect(mocks.writeText).toHaveBeenCalledWith("ssh root@10.0.0.8"),
    );
    expect(mocks.toast).toHaveBeenCalledWith("success", "已复制 SSH 命令：ssh root@10.0.0.8");
  });

  it("非默认端口复制带 -p", async () => {
    openRowMenu(mounted!.container, "edge-1");
    clickMenuItem(mounted!.container, "复制 SSH 命令");
    await waitFor(() =>
      expect(mocks.writeText).toHaveBeenCalledWith("ssh deploy@edge.example.com -p 2222"),
    );
  });

  it("非 SSH 资产不提供复制项与换用户连接项", async () => {
    openRowMenu(mounted!.container, "win-1");
    await flush();
    const labels = menuLabels(mounted!.container);
    expect(labels.some((l) => l.includes("克隆"))).toBe(true);
    expect(labels.some((l) => l.includes("复制 SSH 命令"))).toBe(false);
    expect(labels.some((l) => l.includes("用其他用户名连接"))).toBe(false);
  });

  it("剪贴板失败时提示复制失败", async () => {
    mocks.writeText.mockRejectedValueOnce(new Error("denied"));
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "复制 SSH 命令");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("复制失败")),
    );
  });
});

describe("用其他用户名连接", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("输入用户名后走快速连接, 密码留空用 agent", async () => {
    mocks.promptText.mockResolvedValueOnce("ops").mockResolvedValueOnce("");
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "用其他用户名连接");
    await waitFor(() =>
      expect(mocks.connectQuick).toHaveBeenCalledWith({
        host: "10.0.0.8",
        port: 22,
        username: "ops",
        authKind: "agent",
      }),
    );
    expect(mocks.promptText).toHaveBeenNthCalledWith(1, "用其他用户名连接「web-1」", "root");
  });

  it("输入密码时按密码认证", async () => {
    mocks.promptText.mockResolvedValueOnce("ops").mockResolvedValueOnce("secret");
    openRowMenu(mounted!.container, "edge-1");
    clickMenuItem(mounted!.container, "用其他用户名连接");
    await waitFor(() =>
      expect(mocks.connectQuick).toHaveBeenCalledWith({
        host: "edge.example.com",
        port: 2222,
        username: "ops",
        authKind: "password",
        password: "secret",
      }),
    );
  });

  it("取消输入用户名不发起连接", async () => {
    mocks.promptText.mockResolvedValueOnce(null);
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "用其他用户名连接");
    await flush();
    expect(mocks.connectQuick).not.toHaveBeenCalled();
    expect(mocks.promptText).toHaveBeenCalledTimes(1);
  });

  it("输入空白用户名不发起连接", async () => {
    mocks.promptText.mockResolvedValueOnce("   ");
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "用其他用户名连接");
    await flush();
    expect(mocks.connectQuick).not.toHaveBeenCalled();
  });
});
