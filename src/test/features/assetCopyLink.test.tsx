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
  options: {},
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

describe("asset link copying", () => {
  it("copies nexterm://connect/<id> from the row menu to the clipboard", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() =>
      expect(
        [...mounted!.container.querySelectorAll('[role="treeitem"][title]')].some((r) =>
          (r.getAttribute("title") ?? "").startsWith("web-1"),
        ),
      ).toBe(true),
    );

    openRowMenu(mounted.container, "web-1");
    const item = [...document.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("复制链接"),
    );
    expect(item).toBeTruthy();
    click(item!);

    await waitForRendered(() => expect(mocks.writeText).toHaveBeenCalledWith("nexterm://connect/a1"));
    await waitForRendered(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已复制")),
    );
  });

  it("opens the host detail page from the row menu without connecting", async () => {
    useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [] });
    mounted = mountWithClient(createElement(AssetTree));
    await waitForRendered(() =>
      expect(
        [...mounted!.container.querySelectorAll('[role="treeitem"][title]')].some((r) =>
          (r.getAttribute("title") ?? "").startsWith("web-1"),
        ),
      ).toBe(true),
    );

    openRowMenu(mounted.container, "web-1");
    const item = [...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes("主机详情"));
    expect(item).toBeTruthy();
    click(item!);

    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    expect(tabs.map((t) => [t.kind, t.assetId])).toEqual([["host", "a1"]]);
    expect(useUi.getState().workspaces.some((w) => w.kind === "session")).toBe(false);
  });
});
