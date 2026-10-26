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
  // Quick connect (ad-hoc connection) is a desktop capability; web-mode hiding behavior is covered in quickConnectWeb.test.tsx.
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    mobile: false,
    list: vi.fn(),
    groupList: vi.fn(),
    listCredentials: vi.fn(),
    probeBatch: vi.fn(),
    sessionConnect: vi.fn(),
    dbConnect: vi.fn(),
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
    writeText: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      groupList: mocks.groupList,
      probeBatch: mocks.probeBatch,
      create: mocks.assetCreate,
      knownHostAccept: mocks.knownHostAccept,
    },
    sessionApi: {
      connect: mocks.sessionConnect,
      connectQuick: mocks.sessionConnectQuick,
      quickConnectDefaultUser: mocks.quickConnectDefaultUser,
    },
    dbApi: { connect: mocks.dbConnect },
    vaultApi: {
      status: mocks.vaultStatus,
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
      deleteCredential: mocks.deleteCredential,
    },
    terminalApi: {},
  };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});

vi.mock("../../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/platform")>();
  return { ...actual, isMobile: () => mocks.mobile };
});

import { QuickConnect } from "../../app/QuickConnect";
import {
  assetConnectionProblemKey,
  quickConnectionProblemKey,
  reconnectDeadHost,
  useUi,
} from "../../app/store";
import { sanitizeLayout } from "../../app/layout";
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
const POSTGRES = assetOf({ id: "a-postgres", name: "postgres-01", kind: "postgres", host: "10.0.0.10", port: 5432 });
const REDIS = assetOf({ id: "a-redis", name: "redis-01", kind: "redis", host: "10.0.0.11", port: 6379 });
const LOCAL = assetOf({
  id: "a-local",
  name: "Current device",
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

function mountOverlay(props: { onClose: () => void; kindFilter?: "database" }): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(QuickConnect, props)),
  );
  mountedViews.push(mounted);
  return mounted;
}

async function mountSettled(props: { onClose: () => void; kindFilter?: "database" }): Promise<MountedView> {
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
  mocks.mobile = false;
  mocks.groupList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  useConnectHistory.setState({ entries: {} });
  useAssetReachability.setState({ entries: {}, batchError: null });
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    workspaceRuntime: {},
    connectionProblems: {},
    pushToast: mocks.toast,
  });
  mocks.writeText.mockResolvedValue(undefined);
  Object.defineProperty(window.navigator, "clipboard", {
    value: { writeText: mocks.writeText },
    configurable: true,
  });
  mocks.list.mockResolvedValue([{ ...WEB }, { ...DB }, { ...LOCAL }]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
  mocks.sessionConnect.mockResolvedValue(SESSION);
  mocks.dbConnect.mockResolvedValue({ connId: "db-1" });
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
  useUi.setState({ workspaces: [], activeWorkspaceId: null, connectingAssetIds: [], workspaceRuntime: {} });
});

describe("QuickConnect rendering and recency/frequency ordering", () => {
  it("sorts by connection history and places unconnected assets afterward by name", async () => {
    const now = Date.now();
    useConnectHistory.setState({
      entries: { "a-db": { count: 1, at: now - 60_000 } },
    });
    const view = await mountSettled({ onClose: vi.fn() });
    const names = options(view.container).map((o) => o.textContent ?? "");
    expect(names[0]).toContain("db-01");
    expect(names.some((n) => n.includes("web-01"))).toBe(true);
    expect(names.some((n) => n.includes("Current device"))).toBe(true);
  });

  it("shows the local badge for the built-in local asset and excludes it from probing", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const localRow = options(view.container).find((o) => o.textContent?.includes("Current device"));
    expect(localRow?.textContent).toContain("本机");
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalled());
    const probedIds = mocks.probeBatch.mock.calls[0]?.[0] as string[];
    expect(probedIds).not.toContain("a-local");
  });

  it("omits hidden assets from the list", async () => {
    useAssetVisibility.getState().hide("a-web");
    const view = await mountSettled({ onClose: vi.fn() });
    expect(options(view.container).some((o) => o.textContent?.includes("web-01"))).toBe(false);
  });
});

describe("QuickConnect asset search", () => {
  it("shows the username and target for a uniquely named host", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const row = options(view.container).find((option) => option.textContent?.includes("web-01"));
    expect(row?.textContent).toContain("root@10.0.0.8:22");
  });

  it("shows host identities for same-name assets even while one is connecting", async () => {
    mocks.list.mockResolvedValue([
      { ...WEB, name: "shared" },
      { ...WEB2, name: "shared", username: "deploy" },
    ]);
    useUi.setState({ connectingAssetIds: [WEB2.id] });
    const view = await mountSettled({ onClose: vi.fn() });
    const rows = options(view.container).filter((row) => row.textContent?.includes("shared"));
    expect(rows).toHaveLength(2);
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining("root@10.0.0.8:22"),
      expect.stringContaining("deploy@10.0.0.18:22"),
    ]);
    expect(rows[1]?.textContent).toContain("连接中");
  });

  it("filters by name", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "db-01");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("db-01");
  });

  it("filters by host", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "10.0.0.8");
    await flush();
    const rows = options(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("web-01");
  });

  it("shows an empty-state hint when nothing matches", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "missing asset");
    await flush();
    expect(options(view.container)).toHaveLength(0);
    expect(view.container.textContent).toContain("没有匹配的资产");
  });
});

describe("QuickConnect database entry scope", () => {
  it("shows a database-specific empty state and no SSH actions when there are no database assets", async () => {
    mocks.list.mockResolvedValue([{ ...WEB }, { ...LOCAL }]);
    const view = mountOverlay({ onClose: vi.fn(), kindFilter: "database" });
    await waitFor(() => expect(view.container.textContent).toContain("还没有数据库资产"));
    expect(options(view.container)).toHaveLength(0);
    expect(view.container.textContent).not.toContain("导入 SSH 配置");
    expect(view.container.textContent).not.toContain("打开本机终端");

    setInputValue(searchInput(view.container), "admin@example.com:22");
    await flush();
    expect(options(view.container)).toHaveLength(0);
    expect(view.container.textContent).toContain("没有匹配的数据库资产");
    expect(view.container.textContent).not.toContain("保存为资产并连接");
  });

  it("offers hidden database assets from the empty filtered result", async () => {
    useAssetVisibility.getState().hide(DB.id);
    const view = mountOverlay({ onClose: vi.fn(), kindFilter: "database" });
    await waitFor(() => expect(view.container.textContent).toContain("数据库资产已隐藏"));
    expect(options(view.container)).toHaveLength(0);

    const reveal = [...view.container.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("显示隐藏资产"),
    );
    expect(reveal).toBeTruthy();
    click(reveal!);
    await waitFor(() => expect(options(view.container)).toHaveLength(1));
    expect(options(view.container)[0]?.textContent).toContain("db-01");
  });

  it("shows and connects the sole database asset while hiding SSH and local assets", async () => {
    const onClose = vi.fn();
    const view = await mountSettled({ onClose, kindFilter: "database" });
    expect(options(view.container)).toHaveLength(1);
    expect(options(view.container)[0]?.textContent).toContain("db-01");
    expect(view.container.textContent).not.toContain("web-01");
    expect(view.container.textContent).not.toContain("Current device");

    click(options(view.container)[0]);
    await waitFor(() => expect(mocks.dbConnect).toHaveBeenCalledWith(DB.id));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("lists all supported database kinds and never offers an ad-hoc SSH row", async () => {
    mocks.list.mockResolvedValue([{ ...WEB }, { ...DB }, { ...POSTGRES }, { ...REDIS }]);
    const view = await mountSettled({ onClose: vi.fn(), kindFilter: "database" });
    expect(options(view.container).map((row) => row.textContent)).toEqual([
      expect.stringContaining("db-01"),
      expect.stringContaining("postgres-01"),
      expect.stringContaining("redis-01"),
    ]);
    setInputValue(searchInput(view.container), "admin@example.com:22");
    await flush();
    expect(options(view.container)).toHaveLength(0);
    expect(view.container.textContent).toContain("没有匹配的数据库资产");
  });

  it("rechecks the kind when activating a row whose asset changed after loading", async () => {
    const changed = { ...DB };
    mocks.list.mockResolvedValue([changed]);
    const view = await mountSettled({ onClose: vi.fn(), kindFilter: "database" });
    changed.kind = "ssh";

    click(options(view.container)[0]);
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", "这里只能连接数据库资产");
  });
});

describe("QuickConnect keyboard access", () => {
  it("moves the active option with arrow keys and updates aria-activedescendant", async () => {
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

  it("jumps to the first and last options with Home and End", async () => {
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

  it("connects the active option with Enter and closes with Escape", async () => {
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

  it("traps Tab focus inside the overlay", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const modal = view.container.querySelector<HTMLElement>('[role="dialog"]');
    expect(modal).toBeTruthy();
    for (let i = 0; i < 30; i++) {
      pressKey(document.activeElement ?? document.body, "Tab");
    }
    expect(modal?.contains(document.activeElement)).toBe(true);
  });
});

describe("QuickConnect connection failures and retries", () => {
  it("shows connection errors inline without closing and allows retry", async () => {
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

  it("shows asset-list loading errors and supports retry", async () => {
    mocks.list.mockRejectedValueOnce({ code: "io", message: "list boom" });
    const view = await mountSettled({ onClose: vi.fn() });
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("list boom");
    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(options(view.container).length).toBeGreaterThan(0));
  });
});

describe("QuickConnect in-flight connection guard", () => {
  it("does not duplicate connections on repeated Enter and shows the connecting state inline", async () => {
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

describe("QuickConnect reachability indicators", () => {
  it("renders reachable and unreachable dots from probe results", async () => {
    mocks.probeBatch.mockImplementation(async (ids: string[]) => ({
      results: ids.map((id) =>
        id === "a-web"
          ? { assetId: id, reachable: true, durationMs: 12 }
          : { assetId: id, reachable: false, error: "dial tcp timeout", durationMs: 2500 },
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

  it("shows the checking state while probing", async () => {
    const gate = deferred<{ results: unknown[] }>();
    mocks.probeBatch.mockReturnValueOnce(gate.promise);
    const view = await mountSettled({ onClose: vi.fn() });
    await waitFor(() =>
      expect(view.container.querySelector('[aria-label="可达性检测中"]')).toBeTruthy(),
    );
    gate.resolve({ results: [] });
    await flush();
  });

  it("shows batch probe failures and supports retry", async () => {
    mocks.probeBatch.mockRejectedValueOnce({ code: "io", message: "probe down" });
    const view = await mountSettled({ onClose: vi.fn() });
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("probe down");
    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(2));
  });
});

describe("QuickConnect Retry keyboard activation is not hijacked by the Enter shortcut", () => {
  it("preserves native Enter behavior on a focused retry button after a connection error instead of connecting the active option", async () => {
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

  it("preserves native Enter behavior on a focused retry button after a probe error without connecting", async () => {
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

  it("returns focus to the search input after retry and still closes with Escape", async () => {
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

describe("QuickConnect screen reader and responsive contracts", () => {
  it("provides complete dialog, combobox, and listbox semantics", async () => {
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

  it("uses the constrained-height command overlay class and a scrollable list", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const modal = view.container.querySelector(".nx-command-modal");
    expect(modal?.className).toContain("nx-modal");
    const list = view.container.querySelector('[role="listbox"]');
    expect(list?.className).toContain("max-h-");
    expect(list?.className).toContain("overflow-y-auto");
  });
});

describe("QuickConnect ad-hoc connections", () => {
  it("keeps a non-secret target for dead-session recovery and never changes a remote target to local", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    const input = searchInput(view.container);
    setInputValue(input, "deploy@example.com:2222");
    await flush();
    pressKey(input, "Enter");
    await waitFor(() => expect(useUi.getState().workspaces.length).toBe(1));
    const workspace = useUi.getState().workspaces[0];
    expect(workspace.quickTarget).toMatchObject({
      host: "example.com", port: 2222, username: "deploy", authKind: "password",
    });
    expect(JSON.stringify(workspace)).not.toContain("secret");

    const persisted = sanitizeLayout({
      v: 3, leftOpen: true, leftMode: "assets", rightOpen: false,
      leftWidth: 300, rightWidth: 400, workspaces: [workspace],
      activeWorkspaceId: workspace.id, aiBoards: {},
    });
    expect(persisted?.workspaces[0].quickTarget).toEqual(workspace.quickTarget);
    mocks.sessionConnectQuick.mockClear();
    await reconnectDeadHost(persisted!.workspaces[0]);
    expect(mocks.sessionConnectQuick).toHaveBeenCalledWith({
      host: "example.com", port: 2222, username: "deploy", authKind: "password", password: "secret",
    });
    await expect(reconnectDeadHost({ title: "legacy remote", assetKind: "ssh" })).rejects.toThrow("缺少可恢复的主机目标");
  });

  it("shows ad-hoc and save-as-asset rows for user@host:port, defaulting Enter to the ad-hoc connection", async () => {
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

  it("uses SSH agent authentication when the password is blank", async () => {
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

  it("does not connect or close the overlay when password entry is canceled", async () => {
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

  it("reuses host-key approval, trusts the key, and retries successfully", async () => {
    mocks.sessionConnectQuick
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "host key confirmation required",
        detail: { host: "example.com", port: 2222, keyType: "ssh-ed25519", fingerprint: "SHA256:abc", key: "ZmFrZS1xdWljay1rZXk=" },
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
      "ZmFrZS1xdWljay1rZXk=",
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(2);
  });

  it("shows connection failures inline and allows retry", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce({
      code: "ssh",
      message: "SSH connection to example.com:2222 failed: authentication failed; check the username, password, or SSH agent configuration",
    });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "deploy@example.com:2222");
    await flush();

    pressKey(searchInput(view.container), "Enter");
    await flush();
    const alert = await waitForAlert(view.container);
    expect(alert.textContent).toContain("authentication failed");
    expect(onClose).not.toHaveBeenCalled();

    const retry = [...alert.querySelectorAll("button")].find((b) => b.textContent === "重试");
    click(retry as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("stores the credential and asset before using the existing asset connection flow when saving as an asset", async () => {
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

  it("rolls back the saved credential without leaving an orphan when asset creation fails", async () => {
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

  it("preserves the credential when the asset is created but its connection fails", async () => {
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

  it("uses one asset problem after saving and retries without creating another asset", async () => {
    mocks.list.mockImplementation(async () => [{ ...WEB }, { ...DB }, { ...LOCAL }, { ...CREATED_ASSET }]);
    mocks.sessionConnect
      .mockRejectedValueOnce({ code: "ssh", message: "created connection failed" })
      .mockResolvedValue({ ...SESSION, id: "s-created", assetId: CREATED_ASSET.id, name: CREATED_ASSET.name });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "example.com:2222");
    await flush();

    click(options(view.container)[1] as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledWith(CREATED_ASSET.id));
    await waitFor(() => {
      expect(view.container.querySelector('section[aria-label="本次问题"]')?.textContent)
        .toContain("created connection failed");
    });

    const quickKey = quickConnectionProblemKey({ host: "example.com", port: 2222, username: null });
    const assetKey = assetConnectionProblemKey(CREATED_ASSET.id);
    let state = useUi.getState();
    expect(state.connectionProblems[quickKey]).toBeUndefined();
    expect(state.connectionProblems[assetKey]).toMatchObject({
      connection: { kind: "asset", asset: { id: CREATED_ASSET.id } },
    });
    expect([
      ...Object.values(state.connectionProblems),
      ...Object.values(state.workspaceRuntime).flatMap((runtime) => runtime.problems ?? []),
    ].filter((problem) => problem.connectionKey)).toHaveLength(1);

    const section = view.container.querySelector('section[aria-label="本次问题"]') as HTMLElement;
    const reconnect = [...section.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "重新连接",
    );
    expect(reconnect).toBeDefined();
    click(reconnect!);
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));

    expect(mocks.assetCreate).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    state = useUi.getState();
    expect(state.connectionProblems[quickKey]).toBeUndefined();
    expect(state.connectionProblems[assetKey]).toBeUndefined();
    expect(Object.values(state.workspaceRuntime).flatMap((runtime) => runtime.problems ?? [])).toEqual([]);
  });

  it("resets the active option after a quick target is entered so Enter starts the ad-hoc connection instead of the previous asset row", async () => {
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

  it("keeps asset search without quick-connection rows when input cannot be parsed", async () => {
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

  it("does not duplicate ad-hoc connections on repeated Enter while one is in flight", async () => {
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

describe("QuickConnect first connection and returning paths", () => {
  function actionButton(container: ParentNode, text: string): HTMLButtonElement {
    const button = [...container.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.trim() === text,
    );
    if (!button) throw new Error(`Button not found: ${text}`);
    return button;
  }

  it("offers create, import, and the existing local terminal when there are no remote assets", async () => {
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    const view = mountOverlay({ onClose: vi.fn() });
    await waitFor(() => expect(actionButton(view.container, "新建连接")).toBeTruthy());
    expect(actionButton(view.container, "导入 SSH 配置")).toBeTruthy();
    expect(actionButton(view.container, "打开本机终端")).toBeTruthy();
  });

  it("keeps returning users on direct asset search without first-connection actions", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    expect(options(view.container).length).toBeGreaterThan(0);
    expect(view.container.textContent).not.toContain("新建连接");
    expect(view.container.textContent).not.toContain("导入 SSH 配置");
    expect(view.container.textContent).not.toContain("打开本机终端");
  });

  it("offers create, import, and local terminal in-place when an existing-asset search has no results", async () => {
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "missing asset");
    await flush();
    expect(view.container.textContent).toContain("没有匹配的资产");
    expect(actionButton(view.container, "新建连接")).toBeTruthy();
    expect(actionButton(view.container, "导入 SSH 配置")).toBeTruthy();
    expect(actionButton(view.container, "打开本机终端")).toBeTruthy();
  });

  it("connects a newly saved asset through the existing host-key confirmation flow", async () => {
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "host key confirmation required",
        detail: { host: "example.com", port: 2222, keyType: "ssh-ed25519", fingerprint: "SHA256:abc", key: "ZmFrZS1xdWljay1rZXk=" },
      })
      .mockResolvedValueOnce({ ...SESSION, id: "s-new", assetId: "a-new", name: "example.com:2222" });
    const onClose = vi.fn();
    const view = mountOverlay({ onClose });
    await waitFor(() => expect(actionButton(view.container, "新建连接")).toBeTruthy());
    click(actionButton(view.container, "新建连接"));

    const dialogs = view.container.querySelectorAll<HTMLElement>('[role="dialog"]');
    const editor = dialogs[dialogs.length - 1] as HTMLElement;
    await waitFor(() => expect(editor.textContent).toContain("新建资产"));
    const inputs = editor.querySelectorAll<HTMLInputElement>("input.nx-input");
    setInputValue(inputs[0]!, "example.com:2222");
    setInputValue(inputs[1]!, "example.com");
    click(actionButton(editor, "保存"));

    await waitFor(() => expect(mocks.ask).toHaveBeenCalled());
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "example.com",
      2222,
      "ssh-ed25519",
      "SHA256:abc",
      "ZmFrZS1xdWljay1rZXk=",
    );
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(2));
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("a-new");
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("returns focus to the create action after canceling the asset editor", async () => {
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    const view = mountOverlay({ onClose: vi.fn() });
    await waitFor(() => expect(actionButton(view.container, "新建连接")).toBeTruthy());
    const create = actionButton(view.container, "新建连接");
    create.focus();
    click(create);
    const dialogs = view.container.querySelectorAll<HTMLElement>('[role="dialog"]');
    const editor = dialogs[dialogs.length - 1] as HTMLElement;
    await waitFor(() => expect(editor.textContent).toContain("新建资产"));

    click(actionButton(editor, "取消"));
    await waitFor(() => expect(document.activeElement).toBe(create));
    expect(searchInput(view.container)).toBeTruthy();
  });

  it("uses the existing asset connection flow for the local terminal action", async () => {
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    const onClose = vi.fn();
    const view = mountOverlay({ onClose });
    await waitFor(() => expect(actionButton(view.container, "打开本机终端")).toBeTruthy());
    click(actionButton(view.container, "打开本机终端"));
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledWith("a-local"));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("does not offer local terminal or desktop-only Termius import on mobile", async () => {
    mocks.mobile = true;
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    const view = mountOverlay({ onClose: vi.fn() });
    await waitFor(() => expect(actionButton(view.container, "新建连接")).toBeTruthy());
    expect(view.container.textContent).not.toContain("Current device");
    expect(view.container.textContent).not.toContain("打开本机终端");

    click(actionButton(view.container, "导入 SSH 配置"));
    await waitFor(() => expect(view.container.textContent).toContain("OpenSSH 配置"));
    expect(view.container.textContent).not.toContain("Termius");
  });

  it("hides the local type when creating an asset on mobile", async () => {
    mocks.mobile = true;
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    const view = mountOverlay({ onClose: vi.fn() });
    await waitFor(() => expect(actionButton(view.container, "新建连接")).toBeTruthy());
    click(actionButton(view.container, "新建连接"));
    const dialogs = view.container.querySelectorAll<HTMLElement>('[role="dialog"]');
    const editor = dialogs[dialogs.length - 1] as HTMLElement;
    await waitFor(() => expect(editor.textContent).toContain("新建资产"));

    const type = editor.querySelector<HTMLSelectElement>("select.nx-select")!;
    expect([...type.options].map((option) => option.value)).not.toContain("local");
    expect([...type.options].map((option) => option.value)).toContain("ssh");
  });

  it("does not connect a local asset returned by mobile creation or fall back to another asset", async () => {
    mocks.mobile = true;
    mocks.list.mockResolvedValue([{ ...LOCAL }]);
    mocks.assetCreate.mockResolvedValue(
      assetOf({ id: "a-new-local", name: "unexpected local", kind: "local", host: null, port: null }),
    );
    const onClose = vi.fn();
    const view = mountOverlay({ onClose });
    await waitFor(() => expect(actionButton(view.container, "新建连接")).toBeTruthy());
    click(actionButton(view.container, "新建连接"));
    const dialogs = view.container.querySelectorAll<HTMLElement>('[role="dialog"]');
    const editor = dialogs[dialogs.length - 1] as HTMLElement;
    await waitFor(() => expect(editor.textContent).toContain("新建资产"));
    const inputs = editor.querySelectorAll<HTMLInputElement>("input.nx-input");
    setInputValue(inputs[0]!, "unexpected local");
    setInputValue(inputs[1]!, "example.com");
    click(actionButton(editor, "保存"));

    await waitFor(() => expect(mocks.assetCreate).toHaveBeenCalled());
    await flush();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith(
      "info",
      "移动端不支持本地终端，请双击资产连接远程主机",
    );
  });
});

describe("QuickConnect workspace problems", () => {
  function prepareProblemWorkspace(): void {
    useUi.setState({
      workspaces: [
        {
          id: "ws-connect",
          kind: "session",
          title: "current workspace",
          sessionId: "session-current",
          assetId: "a-web",
          assetKind: "ssh",
          panes: [{ id: "p", tabs: [], activeTabId: null }],
          activePaneId: "p",
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-connect",
      workspaceRuntime: {},
    });
    useUi.getState().ensureWorkspaceRuntime("ws-connect");
  }

  function problems() {
    return useUi.getState().getWorkspaceRuntime("ws-connect")?.problems ?? [];
  }

  function problemSection(container: HTMLElement): HTMLElement {
    const section = container.querySelector<HTMLElement>('section[aria-label="本次问题"]');
    if (!section) throw new Error("problem section not found");
    return section;
  }

  it("keeps the original target for copy and reconnect instead of the newly searched asset", async () => {
    prepareProblemWorkspace();
    mocks.sessionConnect.mockRejectedValueOnce({
      code: "io",
      message: "connection refused; password=fixture-password; token=fixture-token; .env ORDINARY=fixture-env",
    });
    const onClose = vi.fn();
    const view = await mountSettled({ onClose });
    setInputValue(searchInput(view.container), "web-01");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);
    await waitFor(() => expect(problems()).toHaveLength(1));

    expect(problems()[0]).toMatchObject({
      id: expect.any(Number),
      createdAt: expect.any(Number),
      source: "connect",
      target: { assetId: "a-web", title: "web-01" },
    });
    expect(problems()[0].detail).not.toContain("fixture-password");
    expect(problems()[0].detail).not.toContain("fixture-token");
    expect(problems()[0].detail).not.toContain("fixture-env");
    const copy = [...problemSection(view.container).querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "复制",
    );
    click(copy!);
    await waitFor(() => expect(mocks.writeText).toHaveBeenCalledTimes(1));
    const copied = mocks.writeText.mock.calls[0][0] as string;
    expect(copied).toContain("web-01");
    expect(copied).not.toContain("fixture-password");
    expect(copied).not.toContain("fixture-token");
    expect(copied).not.toContain("fixture-env");

    setInputValue(searchInput(view.container), "web-02");
    await flush();
    const reconnect = [...problemSection(view.container).querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "重新连接",
    );
    expect(reconnect).toBeTruthy();
    click(reconnect!);
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("a-web");
  });

  it("offers only copy and details after the original asset disappears", async () => {
    prepareProblemWorkspace();
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "connection refused" });
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "web-01");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);
    await waitFor(() => expect(problems()).toHaveLength(1));
    view.unmount();
    mountedViews.splice(0);
    mocks.list.mockResolvedValue([{ ...WEB2 }, { ...LOCAL }]);

    const reopened = await mountSettled({ onClose: vi.fn() });
    const section = problemSection(reopened.container);
    expect(section.textContent).toContain("web-01");
    expect(section.textContent).toContain("复制");
    expect(section.textContent).toContain("查看详情");
    expect(
      [...section.querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "重新连接",
      ),
    ).toBe(false);
  });

  it("rejects a deferred connection failure after reset", async () => {
    prepareProblemWorkspace();
    const gate = deferred<typeof SESSION>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "web-01");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(1));

    act(() => {
      useUi.getState().resetSessions();
    });
    gate.reject(new Error("late connection failure"));
    await flush();

    expect(problems()).toEqual([]);
  });

  it("rejects a deferred connection failure from the old account runtime", async () => {
    prepareProblemWorkspace();
    const gate = deferred<typeof SESSION>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "web-01");
    await flush();
    click(options(view.container)[0] as HTMLButtonElement);
    await waitFor(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(1));
    const oldGeneration = useUi.getState().getWorkspaceRuntime("ws-connect")!.generation;

    act(() => {
      useUi.getState().resetSessions({ clearWorkspaceRuntime: true });
    });
    const nextGeneration = useUi.getState().getWorkspaceRuntime("ws-connect")!.generation;
    gate.reject(new Error("late connection failure"));
    await flush();

    expect(nextGeneration).not.toBe(oldGeneration);
    expect(problems()).toEqual([]);
    expect(useUi.getState().getWorkspaceRuntime("ws-connect")).toStrictEqual({ generation: nextGeneration });
  });

  it("does not create a problem card when authentication is canceled", async () => {
    prepareProblemWorkspace();
    mocks.promptText.mockResolvedValue(null);
    const view = await mountSettled({ onClose: vi.fn() });
    setInputValue(searchInput(view.container), "deploy@example.com");
    await flush();

    pressKey(searchInput(view.container), "Enter");
    await flush();

    expect(mocks.sessionConnectQuick).not.toHaveBeenCalled();
    expect(problems()).toEqual([]);
    expect(view.container.querySelector('[role="alert"]')).toBeNull();
  });
});
