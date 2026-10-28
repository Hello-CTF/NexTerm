/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    list: vi.fn(),
    probeBatch: vi.fn(),
    sessionConnect: vi.fn(),
    sessionConnectQuick: vi.fn(),
    quickConnectDefaultUser: vi.fn(),
    assetCreate: vi.fn(),
    setCredential: vi.fn(),
    vaultStatus: vi.fn(),
    promptText: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      probeBatch: mocks.probeBatch,
      create: mocks.assetCreate,
    },
    sessionApi: {
      connect: mocks.sessionConnect,
      connectQuick: mocks.sessionConnectQuick,
      quickConnectDefaultUser: mocks.quickConnectDefaultUser,
    },
    dbApi: {},
    vaultApi: { status: mocks.vaultStatus, setCredential: mocks.setCredential },
    terminalApi: {},
  };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, promptText: mocks.promptText };
});

import { JumpPalette } from "../../app/JumpPalette";
import { SshImportDialog } from "../../features/explorer/SshImportDialog";
import { useUi } from "../../app/store";
import { useAssetReachability } from "../../features/explorer/assetReachability";
import type { Asset } from "../../ipc/commands";

const WEB_ASSET: Asset = {
  id: "a-web",
  groupId: null,
  kind: "ssh",
  name: "web-01",
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
};

const CREATED_ASSET: Asset = { ...WEB_ASSET, id: "a-new", name: "example.com:2222", host: "example.com", port: 2222 };

const mountedViews: MountedView[] = [];

function options(container: HTMLElement): HTMLButtonElement[] {
  return [...container.querySelectorAll<HTMLButtonElement>('[role="option"]')];
}

function searchInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("QuickConnect combobox not found");
  return input;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({ sessions: [], workspaces: [], activeWorkspaceId: null, connectingAssetIds: [], pushToast: mocks.toast });
  mocks.list.mockResolvedValue([{ ...WEB_ASSET }]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
  mocks.sessionConnect.mockResolvedValue({ id: "s1", assetId: "a-new", name: "example.com:2222", kind: "ssh", status: "connected", tabs: [], createdAt: 0 });
  mocks.assetCreate.mockResolvedValue({ ...CREATED_ASSET });
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true, mode: "master", autoLockMinutes: 0 });
  mocks.promptText.mockResolvedValue("secret");
});

afterEach(() => {
  for (const view of mountedViews.splice(0)) view.unmount();
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, connectingAssetIds: [] });
});

describe("QuickConnect web mode (server assembly)", () => {
  it("shows only save-as-asset-and-connect for user@host:port, without a temporary connection", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = mount(
      createElement(QueryClientProvider, { client }, createElement(JumpPalette, { onClose: vi.fn(), scope: "hosts" })),
    );
    mountedViews.push(view);
    await waitForRendered(() => expect(options(view.container).length).toBeGreaterThan(0));

    setInputValue(searchInput(view.container), "deploy@example.com:2222");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("保存为资产并连接 deploy@example.com:2222");
    expect(view.container.textContent).not.toContain("临时连接");
  });

  it("does not prefill the server OS user when saving as an asset (session_quick_connect_user is disabled)", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = mount(
      createElement(QueryClientProvider, { client }, createElement(JumpPalette, { onClose: vi.fn(), scope: "hosts" })),
    );
    mountedViews.push(view);
    await waitForRendered(() => expect(options(view.container).length).toBeGreaterThan(0));

    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);

    await waitForRendered(() =>
      expect(mocks.assetCreate).toHaveBeenCalledWith({
        kind: "ssh",
        name: "example.com:2222",
        host: "example.com",
        port: 2222,
        username: "",
        authKind: "password",
        keyPath: null,
        credId: "cred-1",
      }),
    );
    expect(mocks.quickConnectDefaultUser).not.toHaveBeenCalled();
    expect(mocks.sessionConnectQuick).not.toHaveBeenCalled();
  });

  it("offers create and import actions when no assets are available", async () => {
    mocks.list.mockResolvedValue([]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = mount(
      createElement(QueryClientProvider, { client }, createElement(JumpPalette, { onClose: vi.fn(), scope: "hosts" })),
    );
    mountedViews.push(view);
    await waitForRendered(() => expect(view.container.textContent).toContain("新建连接"));
    expect(view.container.textContent).toContain("导入 SSH 配置");
  });

  it("describes default SSH import paths as server files and hides unsupported Termius import", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = mount(
      createElement(QueryClientProvider, { client }, createElement(SshImportDialog, { onClose: vi.fn() })),
    );
    mountedViews.push(view);

    expect(view.container.textContent).toContain("服务器 SSH 配置文件路径");
    expect(view.container.textContent).toContain("不会读取浏览器所在电脑的文件");
    expect(view.container.textContent).not.toContain("Termius");
    click(
      [...view.container.querySelectorAll("button")].find(
        (button) => button.textContent?.trim() === "~/.ssh 目录",
      ) as HTMLButtonElement,
    );
    expect(view.container.textContent).toContain("将扫描服务器上的 ~/.ssh 目录");
  });
});
