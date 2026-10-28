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
  vi.stubGlobal("navigator", {
    ...navigator,
    clipboard: {
      writeText: mocks.writeText,
      readText: () => Promise.resolve(mocks.writeText.mock.calls.at(-1)?.[0] ?? ""),
    },
  });
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

describe("copy SSH command", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("copies ssh user@host for the default port and shows a toast", async () => {
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "复制 SSH 命令");
    await waitFor(() =>
      expect(mocks.writeText).toHaveBeenCalledWith("ssh root@10.0.0.8"),
    );
    expect(mocks.toast).toHaveBeenCalledWith("success", "已复制 SSH 命令：ssh root@10.0.0.8");
  });

  it("includes -p for a non-default port", async () => {
    openRowMenu(mounted!.container, "edge-1");
    clickMenuItem(mounted!.container, "复制 SSH 命令");
    await waitFor(() =>
      expect(mocks.writeText).toHaveBeenCalledWith("ssh deploy@edge.example.com -p 2222"),
    );
  });

  it("does not offer copy or connect-as-user actions for non-SSH assets", async () => {
    openRowMenu(mounted!.container, "win-1");
    await flush();
    const labels = menuLabels(mounted!.container);
    expect(labels.some((l) => l.includes("克隆"))).toBe(true);
    expect(labels.some((l) => l.includes("复制 SSH 命令"))).toBe(false);
    expect(labels.some((l) => l.includes("使用其他用户名连接"))).toBe(false);
  });

  it("shows a copy failure toast when clipboard writing fails", async () => {
    mocks.writeText.mockRejectedValueOnce(new Error("denied"));
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "复制 SSH 命令");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("无法复制")),
    );
  });
});

describe("connect as another user", () => {
  beforeEach(async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(rowByText(mounted!.container, "web-1")).toBeTruthy());
  });

  it("uses quick connect with agent authentication after entering a username and leaving the password blank", async () => {
    mocks.promptText.mockResolvedValueOnce("ops").mockResolvedValueOnce("");
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "使用其他用户名连接");
    await waitFor(() =>
      expect(mocks.connectQuick).toHaveBeenCalledWith({
        host: "10.0.0.8",
        port: 22,
        username: "ops",
        authKind: "agent",
      }),
    );
    expect(mocks.promptText).toHaveBeenNthCalledWith(1, "使用其他用户名连接「web-1」", "root");
  });

  it("uses password authentication when a password is entered", async () => {
    mocks.promptText.mockResolvedValueOnce("ops").mockResolvedValueOnce("secret");
    openRowMenu(mounted!.container, "edge-1");
    clickMenuItem(mounted!.container, "使用其他用户名连接");
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

  it("does not connect when username input is cancelled", async () => {
    mocks.promptText.mockResolvedValueOnce(null);
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "使用其他用户名连接");
    await flush();
    expect(mocks.connectQuick).not.toHaveBeenCalled();
    expect(mocks.promptText).toHaveBeenCalledTimes(1);
  });

  it("does not connect for a blank username", async () => {
    mocks.promptText.mockResolvedValueOnce("   ");
    openRowMenu(mounted!.container, "web-1");
    clickMenuItem(mounted!.container, "使用其他用户名连接");
    await flush();
    expect(mocks.connectQuick).not.toHaveBeenCalled();
  });
});
