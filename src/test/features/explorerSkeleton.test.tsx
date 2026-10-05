/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  connect: vi.fn(),
  assetDelete: vi.fn(),
  ask: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, pickKeyFile: mocks.pickKeyFile };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.assetList,
      search: vi.fn(),
      update: vi.fn(),
      delete: mocks.assetDelete,
      groupList: mocks.groupList,
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      snippetList: mocks.snippetList,
      snippetCreate: vi.fn(),
      snippetUpdate: vi.fn(),
      snippetDelete: vi.fn(),
      readKeyFile: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      status: vi.fn().mockResolvedValue({ initialized: true, unlocked: true }),
      setCredential: vi.fn(),
    },
    sessionApi: {
      connect: mocks.connect,
      probe: vi.fn(),
      list: vi.fn().mockResolvedValue([]),
      connectLocal: vi.fn(),
      reconnect: vi.fn(),
    },
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";

const WEB = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-1",
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function setViewportWidth(width: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ leftOpen: true, pushToast: mocks.toast, workspaces: [], sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setViewportWidth(1024);
});

describe("资产树加载骨架屏", () => {
  it("列表加载中显示骨架屏而不是空态提示", async () => {
    const pending = deferred<typeof WEB[]>();
    mocks.assetList.mockReturnValue(pending.promise);
    mounted = mountWithClient(createElement(AssetTree));
    await flush();

    const status = mounted.container.querySelector('[role="status"][aria-label="正在加载资产"]');
    expect(status).not.toBeNull();
    expect(status!.querySelectorAll(".animate-pulse").length).toBeGreaterThan(0);
    expect(mounted.container.textContent).not.toContain("还没有资产");

    pending.resolve([{ ...WEB }]);
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));
    expect(mounted.container.querySelector('[role="status"]')).toBeNull();
  });

  it("空列表加载完成后骨架屏消失并显示空态提示", async () => {
    const pending = deferred<typeof WEB[]>();
    mocks.assetList.mockReturnValue(pending.promise);
    mounted = mountWithClient(createElement(AssetTree));
    await flush();
    expect(mounted.container.querySelector('[role="status"]')).not.toBeNull();

    pending.resolve([]);
    await waitFor(() => expect(mounted!.container.textContent).toContain("还没有资产"));
    expect(mounted.container.querySelector('[role="status"]')).toBeNull();
  });

  it("骨架条带 reduced-motion 兼容类", async () => {
    const pending = deferred<typeof WEB[]>();
    mocks.assetList.mockReturnValue(pending.promise);
    mounted = mountWithClient(createElement(AssetTree));
    await flush();

    const bars = [...mounted.container.querySelectorAll(".animate-pulse")];
    expect(bars.length).toBeGreaterThan(0);
    for (const bar of bars) {
      expect(bar.className).toContain("motion-reduce:animate-none");
    }
    pending.resolve([]);
  });

  it("320/390 窄屏下骨架行只用比例宽度，不撑破容器", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      const pending = deferred<typeof WEB[]>();
      mocks.assetList.mockReturnValue(pending.promise);
      mounted = mountWithClient(createElement(AssetTree));
      await flush();

      const status = mounted.container.querySelector('[role="status"]');
      expect(status).not.toBeNull();
      expect(status!.closest('[role="tree"]')).not.toBeNull();
      const bars = [...status!.querySelectorAll(".animate-pulse")];
      expect(bars.length).toBeGreaterThan(0);
      for (const bar of bars) {
        expect(bar.className).not.toMatch(/w-\[\d{3,}px\]/);
      }
      expect(bars.some((bar) => /w-\d\/\d/.test(bar.className))).toBe(true);

      pending.resolve([]);
      await waitFor(() => expect(mounted!.container.textContent).toContain("还没有资产"));
      mounted!.unmount();
      mounted = undefined;
    }
  });
});
