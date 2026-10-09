/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

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

import { QuickConnect } from "../../app/QuickConnect";
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

describe("QuickConnect web 模式 (服务端装配)", () => {
  it("输入 user@host:port 只出现「保存为资产并连接」, 不再提供临时连接", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = mount(
      createElement(QueryClientProvider, { client }, createElement(QuickConnect, { onClose: vi.fn() })),
    );
    mountedViews.push(view);
    await waitFor(() => expect(options(view.container).length).toBeGreaterThan(0));

    setInputValue(searchInput(view.container), "deploy@example.com:2222");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("保存为资产并连接 deploy@example.com:2222");
    expect(view.container.textContent).not.toContain("临时连接");
  });

  it("保存为资产不回填服务器 OS 用户 (session_quick_connect_user 已禁用)", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = mount(
      createElement(QueryClientProvider, { client }, createElement(QuickConnect, { onClose: vi.fn() })),
    );
    mountedViews.push(view);
    await waitFor(() => expect(options(view.container).length).toBeGreaterThan(0));

    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);

    await waitFor(() =>
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
});
