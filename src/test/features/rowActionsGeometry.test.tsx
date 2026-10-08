/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  fsList: vi.fn(),
  listenEvent: vi.fn(),
  assetList: vi.fn(),
  groupList: vi.fn(),
  snippetList: vi.fn(),
  listCredentials: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: vi.fn(),
    pickKeyFile: vi.fn(),
    promptText: vi.fn(),
    pickLocalFile: vi.fn(),
    pickSavePath: vi.fn(),
    finishSave: vi.fn(),
    discardStaged: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: {
      list: mocks.fsList,
      read: vi.fn(),
      write: vi.fn(),
      rename: vi.fn(),
      chmod: vi.fn(),
      checksum: vi.fn(),
      mkdir: vi.fn(),
      delete: vi.fn(),
      upload: vi.fn(),
      download: vi.fn(),
      packDownload: vi.fn(),
      extract: vi.fn(),
    },
    assetApi: {
      list: mocks.assetList,
      search: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
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
      connect: vi.fn(),
      probe: vi.fn(),
      list: vi.fn().mockResolvedValue([]),
      connectLocal: vi.fn(),
      reconnect: vi.fn(),
    },
    terminalApi: { write: vi.fn() },
    dbApi: {},
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: mocks.listenEvent };
});

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { FileBrowser } from "../../features/files/FileBrowser";
import { FileTree } from "../../features/files/FileTree";
import { AssetTree } from "../../features/explorer/AssetTree";
import { SnippetsPanel } from "../../features/explorer/SnippetsPanel";
import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const HOME_ENTRIES: FileEntryDto[] = [
  {
    name: "logs",
    path: "~/logs",
    kind: "dir",
    size: 0,
    mode: "drwxr-xr-x",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
  {
    name: "notes.txt",
    path: "~/notes.txt",
    kind: "file",
    size: 12,
    mode: "-rw-r--r--",
    owner: "root",
    group: "root",
    mtime: 1,
    symlinkTarget: null,
  },
];

const GROUP = {
  id: "g1",
  parentId: null,
  name: "生产",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
};

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

const GROUPED = { ...WEB, id: "a3", groupId: "g1", name: "db-1" };

function stubPointer(coarse: boolean): void {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: coarse && query.includes("pointer: coarse"),
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList,
  );
}

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function actionLabels(row: HTMLElement): (string | null)[] {
  return [...row.querySelectorAll<HTMLButtonElement>(".nx-row-actions button")].map((b) =>
    b.getAttribute("aria-label") ?? b.getAttribute("title"),
  );
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.fsList.mockResolvedValue(HOME_ENTRIES.map((e) => ({ ...e })));
  mocks.listenEvent.mockResolvedValue(() => {});
  mocks.assetList.mockResolvedValue([{ ...WEB }, { ...GROUPED }]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([
    { id: "sn1", name: "看容器状态", body: "docker ps", groupId: null, sort: 1 },
  ]);
  useUi.setState({ leftOpen: true, pushToast: vi.fn(), workspaces: [], sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("行内操作条常驻占位的 DOM 契约", () => {
  it("FileTree 行带 nx-row-reserve-actions，操作条按钮常驻 DOM", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find(
      (r) => r.textContent?.includes("notes.txt"),
    );
    if (!row) throw new Error("row not found: notes.txt");
    expect(row.className).toContain("nx-row-reserve-actions");
    expect(actionLabels(row)).toEqual(["下载 notes.txt", "删除 notes.txt", "更多操作 notes.txt"]);
  });

  it("FileBrowser 行带 nx-row-reserve-actions，操作条按钮常驻 DOM", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find(
      (r) => r.textContent?.includes("notes.txt"),
    );
    if (!row) throw new Error("browser row not found: notes.txt");
    expect(row.className).toContain("nx-row-reserve-actions");
    expect(actionLabels(row)).toEqual(["更多操作 notes.txt"]);
  });

  it("资产行带 nx-row-reserve-actions，操作条按钮常驻 DOM", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"][title]')].find(
      (r) => (r.getAttribute("title") ?? "").startsWith("web-1"),
    );
    if (!row) throw new Error("asset row not found: web-1");
    expect(row.className).toContain("nx-row-reserve-actions");
    expect(actionLabels(row)).toEqual([
      "连接 web-1",
      "编辑 web-1",
      "删除 web-1",
      "更多操作 web-1",
    ]);
  });

  it("分组行带 nx-row-reserve-actions，操作条按钮常驻 DOM", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    const header = [...mounted!.container.querySelectorAll<HTMLElement>(".nx-row")].find(
      (r) => r.textContent?.includes("生产") && r.querySelector(".nx-count"),
    );
    if (!header) throw new Error("group header row not found: 生产");
    expect(header.className).toContain("nx-row-reserve-actions");
    expect(actionLabels(header)).toEqual([
      "在分组「生产」内新建资产",
      "重命名分组「生产」",
      "删除分组「生产」",
    ]);
  });

  it("片段行带 nx-row-reserve-actions，操作条按钮常驻 DOM", async () => {
    stubPointer(false);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    const row = [...mounted!.container.querySelectorAll<HTMLElement>(".nx-row")].find((r) =>
      r.textContent?.includes("看容器状态"),
    );
    if (!row) throw new Error("snippet row not found: 看容器状态");
    expect(row.className).toContain("nx-row-reserve-actions");
    expect(actionLabels(row)).toEqual(["插入到当前终端", "编辑片段", "删除片段"]);
  });
});
