/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, mount, waitForRendered, type MountedView } from "../features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  connectLocal: vi.fn(),
  shell: vi.fn(() => "desktop"),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, list: mocks.list, groupList: vi.fn(async () => []) },
    sessionApi: { ...actual.sessionApi, connectLocal: mocks.connectLocal },
    vaultApi: {
      ...actual.vaultApi,
      status: vi.fn(async () => ({ initialized: true, unlocked: true, passwordless: false })),
      listCredentials: vi.fn(async () => []),
    },
  };
});
vi.mock("../../app/shell", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/shell")>()),
  useShell: () => mocks.shell(),
}));

import { WelcomePage } from "../../app/workspace/EmptyStates";
import { recentConnections } from "../../app/workspace/recentConnections";
import { useUi } from "../../app/store";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import type { Asset } from "../../ipc/commands";

function asset(id: string, name: string, kind: Asset["kind"], host: string | null = "10.0.0.1"): Asset {
  return {
    id, groupId: null, kind, name, host, port: null, username: null, authKind: null, keyPath: null, credId: null,
    options: {}, tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null, builtin: false,
  };
}

let mounted: MountedView | undefined;

function mountWelcome(props: Parameters<typeof WelcomePage>[0] = {}): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(WelcomePage, props)));
  return mounted;
}

function buttons(): HTMLButtonElement[] {
  return [...document.querySelectorAll<HTMLButtonElement>("button")];
}

beforeEach(() => {
  document.body.replaceChildren();
  localStorage.clear();
  mocks.shell.mockReturnValue("desktop");
  useConnectHistory.setState({ entries: {} });
  useAssetVisibility.setState({ hiddenIds: [] });
  useUi.setState({ workspaces: [], activeWorkspaceId: null });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

describe("recentConnections", () => {
  it("orders by last connection, skips hidden, missing and local-on-mobile entries, and caps the list", () => {
    const assets = [asset("a", "A", "ssh"), asset("b", "B", "rdp"), asset("c", "C", "ssh"), asset("l", "Local", "local")];
    const history = { a: { count: 1, at: 100 }, b: { count: 1, at: 300 }, c: { count: 1, at: 200 }, l: { count: 1, at: 400 }, gone: { count: 1, at: 500 } };
    expect(recentConnections(assets, history, { hiddenIds: [], mobile: false }).map((r) => r.asset.id)).toEqual(["l", "b", "c", "a"]);
    expect(recentConnections(assets, history, { hiddenIds: ["b"], mobile: true }).map((r) => r.asset.id)).toEqual(["c", "a"]);
    expect(recentConnections(assets, history, { hiddenIds: [], mobile: false, limit: 2 })).toHaveLength(2);
  });
});

describe("WelcomePage", () => {
  it("guides a first run when there are no assets and still offers the entries", async () => {
    mocks.list.mockResolvedValue([]);
    mountWelcome({ onQuickConnect: vi.fn() });
    await waitForRendered(() => expect(document.querySelector('[aria-label="开始使用"]')).toBeTruthy());
    expect(document.querySelector('[aria-label="最近连接"]')).toBeNull();
    expect(buttons().map((b) => b.textContent)).toEqual(expect.arrayContaining(["快速连接", "新建资产"]));
  });

  it("shows an empty recent list gracefully when assets exist but nothing was connected yet", async () => {
    mocks.list.mockResolvedValue([asset("a", "web-1", "ssh")]);
    mountWelcome();
    await waitForRendered(() => expect(document.body.textContent).toContain("还没有连接记录"));
    expect(document.querySelector('[aria-label="开始使用"]')).toBeNull();
  });

  it("lists recent connections from the connect history newest first", async () => {
    mocks.list.mockResolvedValue([asset("a", "web-1", "ssh"), asset("b", "win-1", "rdp", "10.0.0.9")]);
    useConnectHistory.setState({ entries: { a: { count: 3, at: Date.now() - 3_600_000 }, b: { count: 1, at: Date.now() - 60_000 } } });
    mountWelcome();
    await waitForRendered(() => expect(document.body.textContent).toContain("win-1"));
    const names = [...document.querySelectorAll('[aria-label="最近连接"] li')].map((li) => li.textContent ?? "");
    expect(names).toHaveLength(2);
    expect(names[0]).toContain("win-1");
    expect(names[0]).toContain("RDP");
    expect(names[1]).toContain("web-1");
  });

  it("opens an rdp asset as a remote desktop workspace when its row is clicked", async () => {
    mocks.list.mockResolvedValue([asset("r1", "win-1", "rdp")]);
    useConnectHistory.setState({ entries: { r1: { count: 1, at: Date.now() } } });
    mountWelcome();
    await waitForRendered(() => expect(document.body.textContent).toContain("win-1"));
    click(buttons().find((b) => b.textContent?.includes("win-1"))!);
    await waitForRendered(() => expect(useUi.getState().workspaces).toHaveLength(1));
    expect(useUi.getState().workspaces[0]).toMatchObject({ kind: "remote", assetId: "r1", assetKind: "rdp" });
  });

  it("calls the quick connect action and opens the new asset editor", async () => {
    mocks.list.mockResolvedValue([]);
    const onQuickConnect = vi.fn();
    mountWelcome({ onQuickConnect });
    await waitForRendered(() => expect(buttons().some((b) => b.textContent === "快速连接")).toBe(true));
    click(buttons().find((b) => b.textContent === "快速连接")!);
    expect(onQuickConnect).toHaveBeenCalledTimes(1);
    click(buttons().find((b) => b.textContent === "新建资产")!);
    await waitForRendered(() => expect(document.querySelector('[role="dialog"]')).toBeTruthy());
  });

  it("hides the local terminal and the shortcut hints on the mobile shell and keeps the other entries", async () => {
    mocks.shell.mockReturnValue("mobile");
    mocks.list.mockResolvedValue([asset("l", "本机", "local"), asset("a", "web-1", "ssh")]);
    useConnectHistory.setState({ entries: { l: { count: 1, at: 2 }, a: { count: 1, at: 1 } } });
    mountWelcome({ onQuickConnect: vi.fn() });
    await waitForRendered(() => expect(document.body.textContent).toContain("web-1"));
    expect(buttons().some((b) => b.textContent === "打开本地终端")).toBe(false);
    expect(document.querySelector('[aria-label="最近连接"]')!.textContent).not.toContain("本机");
    expect(document.querySelector(".nx-kbd")).toBeNull();
    expect(buttons().some((b) => b.textContent === "新建资产")).toBe(true);
  });

  it("says so when the asset list cannot be loaded", async () => {
    mocks.list.mockRejectedValue(new Error("boom"));
    mountWelcome();
    await waitForRendered(() => expect(document.body.textContent).toContain("无法加载资产列表"));
  });
});
