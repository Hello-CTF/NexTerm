/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, mountWithClient, setInputValue, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  discover: vi.fn(),
  sync: vi.fn(),
  watchStatus: vi.fn(),
  setWatch: vi.fn(),
  groupList: vi.fn(),
  ask: vi.fn(),
  call: vi.fn(),
}));

vi.mock("../../ipc/inventoryApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/inventoryApi")>();
  return {
    ...actual,
    inventoryApi: {
      discover: mocks.discover,
      sync: mocks.sync,
      sshWatchStatus: mocks.watchStatus,
      setSshWatch: mocks.setWatch,
    },
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, groupList: mocks.groupList }, call: mocks.call };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { InventoryDialog } from "../../features/explorer/InventoryDialog";
import { ResetLiveOperationRegistry } from "../../features/explorer/InventoryTargetActions";
import { useUi } from "../../app/store";
import { useAuth } from "../../features/auth/store";

const DISCOVERY = {
  source: "docker-context",
  items: [
    { key: "context/prod", kind: "docker-context", name: "prod", host: "ssh://ops@prod", port: null, username: null, authKind: null, keyPath: null, credId: null, options: {} },
    { key: "context/local", kind: "docker-context", name: "local", host: "unix:///docker.sock", port: null, username: null, authKind: null, keyPath: null, credId: null, options: {} },
  ],
  warnings: [],
};
const DOCKER_TARGET = { type: "context", name: "prod", dockerEndpoint: "ssh://ops@prod.example" };
const EXEC_DISCOVERY = {
  source: "docker-context",
  items: [
    { key: "context/prod", kind: "docker-context", name: "prod", host: "ssh://ops@prod.example", port: null, username: null, authKind: null, keyPath: null, credId: null, options: { inventoryTarget: DOCKER_TARGET } },
  ],
  warnings: [],
};
const K8S_DISCOVERY = {
  source: "kubernetes",
  items: [
    { key: "pod/ops/api-0", kind: "kubernetes", name: "ops/api-0", host: null, port: null, username: null, authKind: null, keyPath: null, credId: null, options: { inventoryTarget: { type: "pod", context: "staging", namespace: "ops", pod: "api-0", container: "app", containers: ["app", "sidecar"] } } },
    { key: "context/staging", kind: "kubernetes", name: "staging", host: null, port: null, username: null, authKind: null, keyPath: null, credId: null, options: { inventoryTarget: { type: "context", name: "staging", cluster: "staging-cluster" } } },
  ],
  warnings: [],
};
const SYNC_RESULT = { groupId: "g1", created: 1, updated: 0, removed: 0, unchanged: 0, warnings: [] };
const DOCKER_SESSION = { id: "sess-1", assetId: "asset-1", name: "prod-host", kind: "docker", status: "connected" as const, tabs: [], createdAt: 1 };

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  ResetLiveOperationRegistry();
  useAuth.setState({ user: null });
  useUi.setState({ sessions: [] });
  mocks.groupList.mockResolvedValue([{ id: "g1", parentId: null, name: "fleet", sort: 0, createdAt: 1, updatedAt: 1 }]);
  mocks.discover.mockResolvedValue(DISCOVERY);
  mocks.sync.mockResolvedValue(SYNC_RESULT);
  mocks.watchStatus.mockResolvedValue({ watching: false, path: "/home/demo/.ssh/config", lastSyncAt: null, lastResult: null, lastError: "" });
  mocks.setWatch.mockResolvedValue({ watching: true, path: "/tmp/config", groupId: "g1", lastSyncAt: 1, lastResult: SYNC_RESULT, lastError: "" });
  mocks.ask.mockResolvedValue(false);
  mounted = mountWithClient(createElement(InventoryDialog, { onClose: () => undefined }));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ accountEpoch: 0, sessions: [] });
  useAuth.setState({ user: null });
  ResetLiveOperationRegistry();
});

describe("dynamic inventory dialog", () => {
  it("discovers read-only candidates and syncs only selected Docker keys", async () => {
    clickButton(mounted!.container, "Docker contexts");
    setSelectValue(mounted!.container.querySelector<HTMLSelectElement>("#inventory-group")!, "g1");
    clickButton(mounted!.container, "预览");
    await waitFor(() => expect(mocks.discover).toHaveBeenCalledWith("docker-context", undefined));
    await waitFor(() => expect(mounted!.container.querySelectorAll('input[aria-label^="选择清单项"]').length).toBe(2));
    const local = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="选择清单项 local"]')!;
    expect(local.checked).toBe(true);
    click(local);
    clickButton(mounted!.container, "同步选中的主机");
    await waitFor(() => expect(mocks.sync).toHaveBeenCalledWith({ source: "docker-context", groupId: "g1", keys: ["context/prod"] }));
    expect(mounted!.container.textContent).toContain("新增 1");
  });

  it("requires confirmation before synchronizing an empty selection", async () => {
    clickButton(mounted!.container, "Docker contexts");
    clickButton(mounted!.container, "预览");
    await waitFor(() => expect(mocks.discover).toHaveBeenCalledWith("docker-context", undefined));
    await waitFor(() => expect(mounted!.container.querySelectorAll('input[aria-label^="选择清单项"]').length).toBe(2));
    clickButton(mounted!.container, "全不选");
    clickButton(mounted!.container, "同步选中的主机");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("移除此来源在当前账号下导入的全部资产"), expect.objectContaining({ kind: "warning" }));
    expect(mocks.sync).not.toHaveBeenCalled();
  });

  it("never syncs when A's empty-selection confirmation resolves true after an account switch", async () => {
    mounted?.unmount();
    // A and B share the one QueryClient, exactly like the running app.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const mountDialog = () =>
      mount(createElement(QueryClientProvider, { client }, createElement(InventoryDialog, { onClose: () => undefined })));

    // A opens the dialog, discovers Docker candidates, and starts an empty-selection sync;
    // the confirmation stays pending across the account switch.
    let resolveAsk!: (value: boolean) => void;
    mocks.ask.mockImplementation(() => new Promise((resolve) => {
      resolveAsk = resolve;
    }));
    mocks.watchStatus.mockResolvedValue({ watching: true, path: "/home/a/.ssh/config", groupId: "g1", lastSyncAt: 1, lastResult: null, lastError: "" });
    mounted = mountDialog();
    clickButton(mounted!.container, "Docker contexts");
    clickButton(mounted!.container, "预览");
    await waitFor(() => expect(mocks.discover).toHaveBeenCalledWith("docker-context", undefined));
    await waitFor(() => expect(mounted!.container.querySelectorAll('input[aria-label^="选择清单项"]').length).toBe(2));
    clickButton(mounted!.container, "全不选");
    clickButton(mounted!.container, "同步选中的主机");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());

    // Account switch: the epoch bumps, the asset tree remounts the dialog, and the account
    // query keys reset (the accountSwitch wiring), all before B loads its own state.
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    mounted?.unmount();
    await act(async () => {
      await client.resetQueries({ queryKey: ["inventory-ssh-watch"] });
    });

    // B loads its own watch status on the same client.
    mocks.watchStatus.mockResolvedValue({ watching: true, path: "/home/b/.ssh/config", groupId: "g1", lastSyncAt: 2, lastResult: null, lastError: "" });
    mounted = mountDialog();
    await waitFor(() =>
      expect(client.getQueryData(["inventory-ssh-watch"])).toMatchObject({ watching: true, path: "/home/b/.ssh/config" }),
    );
    const groupCallsAfterBLoads = mocks.groupList.mock.calls.length;

    // A's confirmation resolves true late: the empty-keys sync must never fire.
    resolveAsk(true);
    await flush();

    expect(mocks.sync).not.toHaveBeenCalled();
    expect(mocks.groupList.mock.calls.length).toBe(groupCallsAfterBLoads);
    expect(client.getQueryData(["inventory-ssh-watch"])).toMatchObject({ watching: true, path: "/home/b/.ssh/config" });
    // A's sync result badges must not render in B's dialog.
    expect(mounted!.container.textContent).not.toContain("新增");
    expect(mounted!.container.textContent).not.toContain("移除 1");
  });

  it("starts SSH watching with the selected path and group", async () => {
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="~/.ssh/config"]')!, "/tmp/config");
    setSelectValue(mounted!.container.querySelector<HTMLSelectElement>("#inventory-group")!, "g1");
    clickButton(mounted!.container, "立即同步并开启自动同步");
    await waitFor(() => expect(mocks.setWatch).toHaveBeenCalledWith({ enabled: true, path: "/tmp/config", groupId: "g1" }));
    expect(mounted!.container.textContent).toContain("关闭自动同步");
    expect(mounted!.container.textContent).toContain("新增 1");
  });

  it("keeps B's watch cache and UI when A's setSshWatch resolves late after an account switch", async () => {
    mounted?.unmount();
    // A and B share the one QueryClient, exactly like the running app.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const mountDialog = () =>
      mount(createElement(QueryClientProvider, { client }, createElement(InventoryDialog, { onClose: () => undefined })));

    // A's watch status loads first.
    mocks.watchStatus.mockResolvedValue({ watching: false, path: "/home/a/.ssh/config", groupId: null, lastSyncAt: null, lastResult: null, lastError: "" });
    mounted = mountDialog();
    await waitFor(() => expect(mocks.watchStatus).toHaveBeenCalled());

    // A starts watching; the RPC stays pending across the account switch.
    let resolveWatch!: (value: unknown) => void;
    mocks.setWatch.mockImplementation(() => new Promise((resolve) => {
      resolveWatch = resolve;
    }));
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="~/.ssh/config"]')!, "/home/a/.ssh/config");
    clickButton(mounted!.container, "立即同步并开启自动同步");
    await waitFor(() => expect(mocks.setWatch).toHaveBeenCalledWith({ enabled: true, path: "/home/a/.ssh/config" }));

    // Account switch: the epoch bumps, the asset tree remounts the dialog, and the account
    // query keys reset (the accountSwitch wiring), all before B loads its own state.
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    mounted?.unmount();
    await act(async () => {
      await client.resetQueries({ queryKey: ["inventory-ssh-watch"] });
    });

    // B loads its own watch status on the same client.
    mocks.watchStatus.mockResolvedValue({ watching: true, path: "/home/b/.ssh/config", groupId: "g1", lastSyncAt: 2, lastResult: null, lastError: "" });
    mounted = mountDialog();
    const pathInput = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="~/.ssh/config"]')!;
    await waitFor(() =>
      expect(client.getQueryData(["inventory-ssh-watch"])).toMatchObject({ watching: true, path: "/home/b/.ssh/config" }),
    );
    // The path-sync effect commits to the DOM on the next flush after the cache write.
    await flush();
    expect(pathInput.value).toBe("/home/b/.ssh/config");

    // A's late resolve must only settle the old component, never write B's cache or UI.
    resolveWatch({ watching: true, path: "/home/a/.ssh/config", groupId: "group-a", lastSyncAt: 1, lastResult: SYNC_RESULT, lastError: "" });
    await flush();

    expect(client.getQueryData(["inventory-ssh-watch"])).toMatchObject({ watching: true, path: "/home/b/.ssh/config" });
    expect(pathInput.value).toBe("/home/b/.ssh/config");
    expect(mounted!.container.textContent).not.toContain("/home/a/.ssh/config");
    expect(mounted!.container.textContent).not.toContain("group-a");
  });

  it("restores the active watch path and group when reopened", async () => {
    mounted?.unmount();
    mocks.watchStatus.mockResolvedValue({
      watching: true,
      path: "/custom/ssh-config",
      groupId: "g1",
      lastSyncAt: 1,
      lastResult: null,
      lastError: "",
    });
    mocks.discover.mockResolvedValue({ source: "ssh-config", items: [], warnings: [] });
    mounted = mountWithClient(createElement(InventoryDialog, { onClose: () => undefined }));
    const path = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="~/.ssh/config"]')!;
    const group = mounted!.container.querySelector<HTMLSelectElement>("#inventory-group")!;
    await waitFor(() => expect(mocks.watchStatus).toHaveBeenCalled());
    await waitFor(() => expect(mounted!.container.textContent).toContain("关闭自动同步"));
    await waitFor(() => {
      expect(path.value).toBe("/custom/ssh-config");
      expect(group.value).toBe("g1");
    });
    clickButton(mounted!.container, "预览");
    await waitFor(() => expect(mocks.discover).toHaveBeenCalledWith("ssh-config", "/custom/ssh-config"));
    clickButton(mounted!.container, "同步全部");
    await waitFor(() => expect(mocks.sync).toHaveBeenCalledWith({ source: "ssh-config", path: "/custom/ssh-config", groupId: "g1" }));
  });

  it("states the Kubernetes inventory-only context and one-shot pod exec boundary", () => {
    clickButton(mounted!.container, "Kubernetes");
    expect(mounted!.container.textContent).toContain("当前 context 下的 pods");
    expect(mounted!.container.textContent).toContain("不会修改集群");
    expect(mounted!.container.textContent).toContain("可以对 pod 执行一次性命令");
    expect(mounted!.container.textContent).toContain("不能打开交互式终端");
  });

  it("executes a discovered docker context target through the dialog", async () => {
    act(() => {
      useUi.setState({ sessions: [DOCKER_SESSION] });
    });
    mocks.discover.mockResolvedValue(EXEC_DISCOVERY);
    clickButton(mounted!.container, "Docker contexts");
    clickButton(mounted!.container, "预览");
    await flush();
    await waitFor(() => expect(mounted!.container.querySelectorAll('input[aria-label^="选择清单项"]').length).toBe(1));

    clickButton(mounted!.container, "执行命令");
    expect(mounted!.container.textContent).toContain("context/prod");
    // Server mode (the web transport under test) requires the caller's own
    // managed session for docker-context exec.
    setInputValue(mounted!.container.querySelector('input[aria-label="命令"]')!, "uname -a");
    setInputValue(mounted!.container.querySelector('input[aria-label="容器"]')!, "web");
    setSelectValue(mounted!.container.querySelector('select[aria-label="受管会话"]')!, "sess-1");
    mocks.call.mockResolvedValue({ output: "Linux\n", exitCode: 0 });
    clickButton(mounted!.container, "执行");
    await flush();

    expect(mocks.call).toHaveBeenCalledTimes(1);
    const [cmd, args] = mocks.call.mock.calls[0];
    expect(cmd).toBe("inventory_target_exec");
    expect(args.args).toMatchObject({
      kind: "docker-context",
      target: DOCKER_TARGET,
      action: "exec",
      container: "web",
      command: ["uname", "-a"],
      sessionId: "sess-1",
    });
    await waitFor(() => expect(mounted!.container.textContent).toContain("Linux"));
    expect(mounted!.container.textContent).toContain("退出码 0");
  });

  it("offers exec only for executable kubernetes candidates", async () => {
    mocks.discover.mockResolvedValue(K8S_DISCOVERY);
    clickButton(mounted!.container, "Kubernetes");
    clickButton(mounted!.container, "预览");
    await flush();
    await waitFor(() => expect(mounted!.container.querySelectorAll('input[aria-label^="选择清单项"]').length).toBe(2));

    // The pod candidate offers the exec disclosure; the context candidate does not.
    const disclosures = [...mounted!.container.querySelectorAll("button")].filter((button) => button.textContent?.trim() === "执行命令");
    expect(disclosures.length).toBe(1);
    clickButton(mounted!.container, "执行命令");
    expect(mounted!.container.textContent).toContain("ops/api-0");
    const select = mounted!.container.querySelector<HTMLSelectElement>('select[aria-label="容器"]')!;
    expect([...select.options].map((option) => option.value)).toEqual(["app", "sidecar"]);
    // Kubernetes pod exec runs through the server-side gate, no managed session.
    expect(mounted!.container.querySelector('select[aria-label="受管会话"]')).toBeNull();
  });

  it("keeps an in-flight exec bound to its launching account across an account switch", async () => {
    act(() => {
      useAuth.setState({ user: { id: "user-a", username: "a", display_name: "A", role: "user", state: "active", must_change_password: false, mfa_enabled: false, created_at: 1, updated_at: 1, last_login_at: 1 } });
      useUi.setState({ sessions: [DOCKER_SESSION] });
    });
    mocks.discover.mockResolvedValue(EXEC_DISCOVERY);
    clickButton(mounted!.container, "Docker contexts");
    clickButton(mounted!.container, "预览");
    await flush();
    await waitFor(() => expect(mounted!.container.querySelectorAll('input[aria-label^="选择清单项"]').length).toBe(1));
    clickButton(mounted!.container, "执行命令");
    setInputValue(mounted!.container.querySelector('input[aria-label="命令"]')!, "true");
    setInputValue(mounted!.container.querySelector('input[aria-label="容器"]')!, "web");
    setSelectValue(mounted!.container.querySelector('select[aria-label="受管会话"]')!, "sess-1");
    let resolveExec!: (value: { output: string; exitCode: number }) => void;
    mocks.call.mockImplementation(() => new Promise<{ output: string; exitCode: number }>((resolve) => { resolveExec = resolve; }));
    clickButton(mounted!.container, "执行");
    await flush();
    expect(mounted!.container.textContent).toContain("执行中");

    // Account switch: the tracked operation stays bound to user-a with the
    // switch-back note, and its late settle never renders on user-b's page.
    act(() => {
      useAuth.setState({ user: { id: "user-b", username: "b", display_name: "B", role: "user", state: "active", must_change_password: false, mfa_enabled: false, created_at: 1, updated_at: 1, last_login_at: 1 } });
    });
    await flush();
    expect(mounted!.container.textContent).toContain("切换回发起该操作的账号后才能取消");

    await act(async () => { resolveExec({ output: "A-SECRET\n", exitCode: 0 }); });
    await flush();
    expect(mounted!.container.textContent).not.toContain("A-SECRET");
  });
});
