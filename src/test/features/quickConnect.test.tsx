/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  deferred,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  // 快速连接(临时连接)是桌面端能力; web 模式的隐藏行为见 quickConnectWeb.test.tsx。
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    list: vi.fn(),
    probeBatch: vi.fn(),
    sessionConnect: vi.fn(),
    sessionConnectQuick: vi.fn(),
    quickConnectDefaultUser: vi.fn(),
    assetCreate: vi.fn(),
    knownHostAccept: vi.fn(),
    setCredential: vi.fn(),
    deleteCredential: vi.fn(),
    vaultStatus: vi.fn(),
    ask: vi.fn(),
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
      knownHostAccept: mocks.knownHostAccept,
    },
    sessionApi: {
      connect: mocks.sessionConnect,
      connectQuick: mocks.sessionConnectQuick,
      quickConnectDefaultUser: mocks.quickConnectDefaultUser,
    },
    dbApi: {},
    vaultApi: { status: mocks.vaultStatus, setCredential: mocks.setCredential, deleteCredential: mocks.deleteCredential },
    terminalApi: {},
  };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});

import { QuickConnect } from "../../app/QuickConnect";
import { useUi } from "../../app/store";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { useAssetReachability } from "../../features/explorer/assetReachability";
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

const WEB = assetOf({ id: "a-web", name: "web-01" });
const WEB2 = assetOf({ id: "a-web2", name: "web-02", host: "10.0.0.18" });
const DB = assetOf({ id: "a-db", name: "db-01", kind: "mysql", host: "10.0.0.9", port: 3306 });
const LOCAL = assetOf({
  id: "a-local",
  name: "当前设备",
  kind: "local",
  host: null,
  port: null,
  builtin: true,
});

const SESSION = {
  id: "s1",
  assetId: "a-web",
  name: "web-01",
  kind: "ssh",
  status: "connected" as const,
  tabs: [],
  createdAt: 0,
};

const QUICK_SESSION = {
  id: "s-quick",
  assetId: null,
  name: "deploy@example.com:2222",
  kind: "ssh",
  status: "connected" as const,
  tabs: [],
  createdAt: 0,
};

const CREATED_ASSET = assetOf({
  id: "a-new",
  name: "example.com:2222",
  host: "example.com",
  port: 2222,
  username: "ubuntu",
  authKind: "password",
  credId: "cred-1",
});

let mounted: MountedView | undefined;
const mountedViews: MountedView[] = [];

function mountOverlay(props: { onClose: () => void }): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(QuickConnect, props)),
  );
  mountedViews.push(mounted);
  return mounted;
}

async function mountSettled(props: { onClose: () => void }): Promise<MountedView> {
  const view = mountOverlay(props);
  await waitFor(() => {
    if (
      options(view.container).length === 0 &&
      !view.container.querySelector('[role="alert"]')
    ) {
      throw new Error("overlay not settled");
    }
  });
  return view;
}

async function waitForAlert(container: HTMLElement): Promise<HTMLElement> {
  await waitFor(() => expect(container.querySelector('[role="alert"]')).toBeTruthy());
  return container.querySelector('[role="alert"]') as HTMLElement;
}

function options(container: HTMLElement): HTMLButtonElement[] {
  return [...container.querySelectorAll<HTMLButtonElement>('[role="option"]')];
}

function searchInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("QuickConnect combobox not found");
  return input;
}

function pressKey(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  useConnectHistory.setState({ entries: {} });
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...DB }, { ...LOCAL }]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
  mocks.sessionConnect.mockResolvedValue(SESSION);
  mocks.sessionConnectQuick.mockResolvedValue(QUICK_SESSION);
  mocks.quickConnectDefaultUser.mockResolvedValue("ubuntu");
  mocks.assetCreate.mockResolvedValue({ ...CREATED_ASSET });
  mocks.knownHostAccept.mockResolvedValue(undefined);
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  mocks.deleteCredential.mockResolvedValue(undefined);
  mocks.vaultStatus.mockResolvedValue({
    initialized: true,
    unlocked: true,
    mode: "master",
    autoLockMinutes: 0,
  });
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue("secret");
});

afterEach(() => {
  for (const view of mountedViews.splice(0)) view.unmount();
  mounted = undefined;
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, connectingAssetIds: [] });
});

describe("QuickConnect 渲染与最近/频次排序", () => {
  it("按连接历史排序，未连接资产按名称排在后面", async () => {
    const now = Date.now();
    useConnectHistory.setState({
      entries: { "a-db": { count: 1, at: now - 60_000 } },
    });
    const view = await mountSettled({ onClose: vi.fn() });
    const names = options(view.container).map((o) => o.textContent ?? "");
    expect(names[0]).toContain("db-01");
    expect(names.some((n) => n.includes("web-01"))).toBe(true);
    expect(names.some((n) => n.includes("当前设备"))).toBe(true);
  });

  it("本机资产显示「本机」徽章且不参与探测", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const localRow = options(view.container).find((o) => o.textContent?.includes("当前设备"));
    expect(localRow?.textContent).toContain("本机");
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalled());
    const probedIds = mocks.probeBatch.mock.calls[0]?.[0] as string[];
    expect(probedIds).not.toContain("a-local");
  });

  it("隐藏的资产不出现在列表中", async () => {
    useAssetVisibility.getState().hide("a-web");
    const view = await mountSettled({ onClose: vi.fn() });
    expect(options(view.container).some((o) => o.textContent?.includes("web-01"))).toBe(false);
  });
});

describe("QuickConnect 资产搜索", () => {
  it("按名称过滤", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "db-01");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("db-01");
  });

  it("按主机过滤", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "10.0.0.8");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("web-01");
  });

  it("无匹配时给出空态提示", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "不存在的资产");
    await flush();
    expect(options(view.container)).toHaveLength(0);
    expect(view.container.textContent).toContain("没有匹配的资产");
  });
});

describe("QuickConnect 键盘可达", () => {
  it("方向键移动活动项并同步 aria-activedescendant", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const input = searchInput(view.container);
    expect(input.getAttribute("aria-activedescendant")).toBe(options(view.container)[0]?.id);

    pressKey(input, "ArrowDown");
    await flush();
    const rows = options(view.container);
    expect(input.getAttribute("aria-activedescendant")).toBe(rows[1]?.id);
    expect(rows[1]?.getAttribute("aria-selected")).toBe("true");

    pressKey(input, "ArrowUp");
    await flush();
    expect(input.getAttribute("aria-activedescendant")).toBe(rows[0]?.id);
  });

  it("Home/End 跳到首尾", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const input = searchInput(view.container);
    const rows = options(view.container);
    pressKey(input, "End");
    await flush();
    expect(input.getAttribute("aria-activedescendant")).toBe(rows[rows.length - 1]?.id);
    pressKey(input, "Home");
    await flush();
    expect(input.getAttribute("aria-activedescendant")).toBe(rows[0]?.id);
  });

  it("Enter 连接活动项，Escape 关闭", async () => {
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    const input = searchInput(view.container);
    setInputValue(input, "web-01");
    await flush();
    pressKey(input, "Enter");
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledWith("a-web"));
    await waitFor(() => expect(onClose).toHaveBeenCalled());

    const onClose2 = vi.fn();
    const view2 = await mountSettled({ onClose: onClose2 });
    pressKey(searchInput(view2.container), "Escape");
    expect(onClose2).toHaveBeenCalledTimes(1);
  });

  it("Tab 焦点被困在弹层内", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const modal = view.container.querySelector<HTMLElement>('[role="dialog"]');
    expect(modal).toBeTruthy();
    for (let i = 0; i < 30; i++) {
      pressKey(document.activeElement ?? document.body, "Tab");
    }
    expect(modal?.contains(document.activeElement)).toBe(true);
  });
});

describe("QuickConnect 连接失败与重试（failure-state）", () => {
  it("连接失败时内联展示错误且不关闭，重试可再次发起", async () => {
    const onClose = vi.fn();
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "connection refused" });
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "web-01");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(1));

    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("connection refused");
    expect(onClose).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalledWith("error", expect.anything());

    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    expect(retry).toBeTruthy();
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("资产列表加载失败时展示错误并可重试", async () => {
    mocks.list.mockRejectedValueOnce({ code: "io", message: "list boom" });
    const view = await mountSettled({ onClose: vi.fn() });
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("list boom");
    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(options(view.container).length).toBeGreaterThan(0));
  });
});

describe("QuickConnect in-flight 连接守卫", () => {
  it("在途期间重复 Enter 不重复发起连接，行内显示连接中", async () => {
    const gate = deferred<typeof SESSION>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    const input = searchInput(view.container);
    setInputValue(input, "web-01");
    await flush();

    pressKey(input, "Enter");
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(1));
    pressKey(input, "Enter");
    await flush();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(options(view.container)[0]?.textContent).toContain("连接中…");
    expect(onClose).not.toHaveBeenCalled();

    gate.resolve(SESSION);
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
});

describe("QuickConnect 可达性指示", () => {
  it("探测结果以可达/不可达圆点呈现", async () => {
    mocks.probeBatch.mockImplementation(async (ids: string[]) => ({
      results: ids.map((id) =>
        id === "a-web"
          ? { assetId: id, reachable: true, durationMs: 12 }
          : { assetId: id, reachable: false, error: "dial tcp 超时", durationMs: 2500 },
      ),
    }));
    const view = await mountSettled({ onClose: vi.fn() });
    await waitFor(() => {
      const labels = [...view.container.querySelectorAll('[role="img"]')].map((el) =>
        el.getAttribute("aria-label"),
      );
      expect(labels).toContain("可达");
      expect(labels).toContain("不可达");
    });
    const probedIds = mocks.probeBatch.mock.calls[0]?.[0] as string[];
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
    expect(probedIds).toEqual(["a-db", "a-web"]);
    expect(mocks.probeBatch.mock.calls[0]?.[1]).toBe(2500);
    expect(mocks.probeBatch.mock.calls[0]?.[2]).toBe(8);
  });

  it("探测期间显示检测中状态", async () => {
    const gate = deferred<{ results: unknown[] }>();
    mocks.probeBatch.mockReturnValueOnce(gate.promise);
    const view = await mountSettled({ onClose: vi.fn() });
    await waitFor(() =>
      expect(view.container.querySelector('[aria-label="可达性检测中"]')).toBeTruthy(),
    );
    gate.resolve({ results: [] });
    await flush();
  });

  it("批量探测失败时展示错误并可重试", async () => {
    mocks.probeBatch.mockRejectedValueOnce({ code: "io", message: "probe down" });
    const view = await mountSettled({ onClose: vi.fn() });
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("probe down");
    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(2));
  });
});

describe("QuickConnect Retry 按钮键盘激活不被 Enter 快捷键劫持", () => {
  it("连接错误后聚焦重试按 Enter 保留原生行为，不连当前活动项", async () => {
    mocks.list.mockResolvedValue([{ ...WEB }, { ...WEB2 }, { ...LOCAL }]);
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "connection refused" });
    const view = await mountSettled({ onClose: vi.fn() });
    const input = searchInput(view.container);
    setInputValue(input, "web-01");
    await flush();
    pressKey(input, "Enter");
    const alert = await waitForAlert(view.container);
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("a-web");

    setInputValue(input, "web-02");
    await flush();
    const activeId = input.getAttribute("aria-activedescendant");
    const activeRow = document.getElementById(activeId ?? "");
    expect(activeRow?.textContent).toContain("web-02");

    const retry = [...alert.querySelectorAll("button")].find(
      (b) => b.textContent === "重试",
    ) as HTMLButtonElement;
    retry.focus();
    const event = pressKey(retry, "Enter");
    expect(event.defaultPrevented).toBe(false);
    await flush();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnect).not.toHaveBeenCalledWith("a-web2");
  });

  it("探测错误后聚焦重试按 Enter 保留原生行为，不触发连接", async () => {
    mocks.probeBatch.mockRejectedValueOnce({ code: "io", message: "probe down" });
    const view = await mountSettled({ onClose: vi.fn() });
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("probe down");
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);

    const retry = [...alert.querySelectorAll("button")].find(
      (b) => b.textContent === "重试",
    ) as HTMLButtonElement;
    retry.focus();
    const event = pressKey(retry, "Enter");
    expect(event.defaultPrevented).toBe(false);
    await flush();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
  });

  it("重试后焦点回到搜索框，Escape 仍可关闭", async () => {
    mocks.sessionConnect.mockRejectedValue({ code: "io", message: "down" });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    const input = searchInput(view.container);
    setInputValue(input, "web-01");
    await flush();
    pressKey(input, "Enter");
    const alert = await waitForAlert(view.container);

    const retry = [...alert.querySelectorAll("button")].find(
      (b) => b.textContent === "重试",
    ) as HTMLButtonElement;
    retry.focus();
    click(retry);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(2));
    expect(document.activeElement).toBe(input);

    pressKey(input, "Escape");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("QuickConnect 屏幕阅读器与响应式契约", () => {
  it("弹层具备完整 dialog/combobox/listbox 语义", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const dialog = view.container.querySelector('[role="dialog"]');
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(dialog?.getAttribute("aria-labelledby")).toBeTruthy();
    const input = searchInput(view.container);
    expect(input.getAttribute("aria-controls")).toBe(
      view.container.querySelector('[role="listbox"]')?.id,
    );
    expect(input.getAttribute("aria-expanded")).toBe("true");
    for (const option of options(view.container)) {
      expect(option.getAttribute("aria-selected")).toMatch(/true|false/);
    }
  });

  it("弹层使用受限高的命令弹层类，列表可滚动", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const modal = view.container.querySelector(".nx-command-modal");
    expect(modal?.className).toContain("nx-modal");
    const list = view.container.querySelector('[role="listbox"]');
    expect(list?.className).toContain("max-h-");
    expect(list?.className).toContain("overflow-y-auto");
  });
});

describe("QuickConnect 快速连接", () => {
  it("输入 user@host:port 时出现临时连接与保存为资产两行，Enter 默认临时连接", async () => {
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    const input = searchInput(view.container);
    setInputValue(input, "deploy@example.com:2222");
    await flush();

    const rows = options(view.container);
    expect(rows[0]?.textContent).toContain("临时连接 deploy@example.com:2222");
    expect(rows[1]?.textContent).toContain("保存为资产并连接 deploy@example.com:2222");
    expect(input.getAttribute("aria-activedescendant")).toBe(rows[0]?.id);

    pressKey(input, "Enter");
    await waitFor(() =>
      expect(mocks.sessionConnectQuick).toHaveBeenCalledWith({
        host: "example.com",
        port: 2222,
        username: "deploy",
        authKind: "password",
        password: "secret",
      }),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.promptText).toHaveBeenCalledWith(
      "输入 deploy@example.com:2222 的登录密码（留空则使用 SSH agent）",
      "",
      { secret: true },
    );
    expect(mocks.assetCreate).not.toHaveBeenCalled();
    expect(mocks.setCredential).not.toHaveBeenCalled();
  });

  it("密码留空时使用 SSH agent 认证", async () => {
    mocks.promptText.mockResolvedValue("");
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();

    pressKey(searchInput(view.container), "Enter");
    await waitFor(() =>
      expect(mocks.sessionConnectQuick).toHaveBeenCalledWith({
        host: "example.com",
        port: 2222,
        authKind: "agent",
      }),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("取消密码输入不发起连接也不关闭弹层", async () => {
    mocks.promptText.mockResolvedValue(null);
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "deploy@example.com");
    await flush();

    pressKey(searchInput(view.container), "Enter");
    await flush();
    expect(mocks.sessionConnectQuick).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(view.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("复用 host key 审批：确认后信任并重试成功", async () => {
    mocks.sessionConnectQuick
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "主机指纹待确认",
        detail: { host: "example.com", port: 2222, keyType: "ssh-ed25519", fingerprint: "SHA256:abc" },
      })
      .mockResolvedValueOnce(QUICK_SESSION);
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "deploy@example.com:2222");
    await flush();

    pressKey(searchInput(view.container), "Enter");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "example.com",
      2222,
      "ssh-ed25519",
      "SHA256:abc",
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(2);
  });

  it("连接失败时内联展示错误，重试可再次发起", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce({
      code: "ssh",
      message: "SSH 连接 example.com:2222 失败：认证失败，请检查用户名、密码或 SSH agent 配置",
    });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "deploy@example.com:2222");
    await flush();

    pressKey(searchInput(view.container), "Enter");
    await flush();
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("认证失败");
    expect(onClose).not.toHaveBeenCalled();

    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("保存为资产并连接：落库凭据与资产后走既有资产连接流程", async () => {
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();

    const saveRow = options(view.container)[1] as HTMLButtonElement;
    expect(saveRow.textContent).toContain("保存为资产并连接");
    click(saveRow);

    await waitFor(() =>
      expect(mocks.setCredential).toHaveBeenCalledWith("example.com:2222", "password", "secret"),
    );
    await waitFor(() =>
      expect(mocks.assetCreate).toHaveBeenCalledWith({
        kind: "ssh",
        name: "example.com:2222",
        host: "example.com",
        port: 2222,
        username: "ubuntu",
        authKind: "password",
        keyPath: null,
        credId: "cred-1",
      }),
    );
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledWith("a-new"));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.sessionConnectQuick).not.toHaveBeenCalled();
    expect(mocks.quickConnectDefaultUser).toHaveBeenCalled();
    expect(mocks.deleteCredential).not.toHaveBeenCalled();
  });

  it("资产创建失败时回滚已保存的凭据，不留孤儿凭据", async () => {
    mocks.assetCreate.mockRejectedValueOnce({ code: "io", message: "asset create boom" });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();

    click(options(view.container)[1] as HTMLButtonElement);
    await waitFor(() => expect(mocks.setCredential).toHaveBeenCalled());
    await waitFor(() => expect(mocks.deleteCredential).toHaveBeenCalledWith("cred-1"));
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("asset create boom");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("资产已创建但连接失败时保留凭据", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({ code: "ssh", message: "connect boom" });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();

    click(options(view.container)[1] as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledWith("a-new"));
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("connect boom");
    expect(mocks.deleteCredential).not.toHaveBeenCalled();
  });

  it("输入 quick target 后重置光标，Enter 激活临时连接而不是旧资产行", async () => {
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    const input = searchInput(view.container);

    pressKey(input, "ArrowDown");
    await flush();
    const assetRow = options(view.container)[1];
    expect(input.getAttribute("aria-activedescendant")).toBe(assetRow?.id);

    setInputValue(input, "deploy@example.com:2222");
    await flush();
    const rows = options(view.container);
    expect(rows[0]?.textContent).toContain("临时连接 deploy@example.com:2222");
    expect(input.getAttribute("aria-activedescendant")).toBe(rows[0]?.id);

    pressKey(input, "Enter");
    await waitFor(() =>
      expect(mocks.sessionConnectQuick).toHaveBeenCalledWith({
        host: "example.com",
        port: 2222,
        username: "deploy",
        authKind: "password",
        password: "secret",
      }),
    );
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("输入无法解析时不出现快速连接行，保持资产搜索", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "web-01");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("web-01");
    expect(view.container.textContent).not.toContain("临时连接");

    setInputValue(searchInput(view.container), "10.0.0.8");
    await flush();
    expect(options(view.container)).toHaveLength(1);
    expect(options(view.container)[0]?.textContent).toContain("web-01");
  });

  it("在途期间重复 Enter 不重复发起快速连接", async () => {
    const gate = deferred<typeof QUICK_SESSION>();
    mocks.sessionConnectQuick.mockReturnValueOnce(gate.promise);
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    const input = searchInput(view.container);
    setInputValue(input, "deploy@example.com:2222");
    await flush();

    pressKey(input, "Enter");
    await waitFor(() => expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(1));
    pressKey(input, "Enter");
    await flush();
    expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(1);
    expect(options(view.container)[0]?.textContent).toContain("连接中…");

    gate.resolve(QUICK_SESSION);
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
});
