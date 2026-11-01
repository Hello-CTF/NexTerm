/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
  writeText: vi.fn(),
  toast: vi.fn(),
  wake: vi.fn(),
}));

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
      search: vi.fn(),
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      groupList: mocks.groupList,
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      snippetList: mocks.snippetList,
      readKeyFile: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: vi.fn(),
      revealCredential: vi.fn(),
    },
    sessionApi: { probe: vi.fn(), connect: vi.fn(), list: vi.fn() },
    terminalApi: { write: vi.fn() },
    dbApi: {},
    wakeApi: { wake: mocks.wake },
  };
});

import { AssetTree } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";
import type { Asset } from "../../ipc/commands";

const WEB = {
  id: "a1",
  name: "web-1",
  groupId: null,
  kind: "ssh",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
  keyPath: null,
  credId: "cred-1",
  options: { wakeMac: "AA:BB:CC:DD:EE:01" },
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
} satisfies Asset;

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function openRowMenu(container: HTMLElement, name: string): void {
  click(
    [...container.querySelectorAll("button")].find(
      (b) => b.getAttribute("aria-label") === `更多操作 ${name}`,
    ) ?? (() => { throw new Error(`More button not found: ${name}`); })(),
  );
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText: mocks.writeText },
    configurable: true,
  });
  mocks.writeText.mockResolvedValue(undefined);
  mocks.list.mockResolvedValue([{ ...WEB }]);
  mocks.groupList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  useUi.setState({ leftOpen: true, pushToast: mocks.toast });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function menuLabels(): string[] {
  return [...document.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent ?? "");
}

describe("asset wake action", () => {
  it("sends a wake request for the asset id and tells the user a sent packet does not mean the host woke", async () => {
    mocks.wake.mockResolvedValue({ assetId: "a1", route: "local", target: "255.255.255.255:9", mac: "AA:BB:CC:DD:EE:01", bytes: 102, sentAtMs: 1 });
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="treeitem"][title]')).toBeTruthy());
    openRowMenu(mounted.container, "web-1");
    const item = [...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes("唤醒"));
    expect(item).toBeTruthy();
    click(item!);
    await waitForRendered(() => expect(mocks.wake).toHaveBeenCalledWith("a1"));
    await waitForRendered(() => expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("不代表主机已经唤醒")));
  });

  it("shows a failed wake as an error", async () => {
    mocks.wake.mockRejectedValue({ code: "forbidden", message: "无权访问该资产" });
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="treeitem"][title]')).toBeTruthy());
    openRowMenu(mounted.container, "web-1");
    click([...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes("唤醒"))!);
    await waitForRendered(() => expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("无权访问该资产")));
  });

  it("hides the action when the asset has no wake MAC", async () => {
    mocks.list.mockResolvedValue([{ ...WEB, options: {} }]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="treeitem"][title]')).toBeTruthy());
    openRowMenu(mounted.container, "web-1");
    expect(menuLabels().some((label) => label.includes("唤醒"))).toBe(false);
    expect(menuLabels().some((label) => label.includes("编辑"))).toBe(true);
  });
});
