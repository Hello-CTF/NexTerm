/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  deferred,
  flushUntil,
  mount,
  waitFor,
  type MountedView,
} from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  fsList: vi.fn(),
  dockerPs: vi.fn(),
  dockerImages: vi.fn(),
  dockerOverview: vi.fn(),
  listLive: vi.fn(),
  listenEvent: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
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
    dockerApi: {
      ps: mocks.dockerPs,
      images: mocks.dockerImages,
      overview: mocks.dockerOverview,
      inspect: vi.fn(),
      stats: vi.fn(),
      containerListDir: vi.fn(),
      action: vi.fn(),
      imageRemove: vi.fn(),
      imagePull: vi.fn(),
      logsAttach: vi.fn(),
      execAttach: vi.fn(),
    },
    terminalApi: { listLive: mocks.listLive, write: vi.fn(), closeTab: vi.fn() },
    sessionApi: { list: vi.fn().mockResolvedValue([]) },
  };
});
vi.mock("../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    createBinaryChannel: vi.fn(),
    disposeChannel: vi.fn(),
    onChannelReopen: vi.fn(() => () => undefined),
  };
});
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

import { FileBrowser } from "../features/files/FileBrowser";
import { FileTree } from "../features/files/FileTree";
import { DockerPanel } from "../features/docker/DockerPanel";
import { BackgroundSessions } from "../features/terminal/BackgroundSessions";
import { useUi } from "../app/store";
import type { FileEntryDto } from "../ipc/types";
import type { ContainerSummary, ImageSummary, LiveTabInfo } from "../ipc/commands";

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

const CONTAINERS: ContainerSummary[] = [
  {
    id: "aaaa11112222",
    name: "web",
    image: "nginx:1.25",
    state: "running",
    status: "Up 2 hours",
    ports: "80/tcp",
    composeProject: null,
  },
];

const IMAGES: ImageSummary[] = [
  { id: "img-1", repository: "nginx", tag: "1.25", size: "187MB", createdSince: "2 days ago" },
];

const LIVE_TABS: LiveTabInfo[] = [
  {
    tabId: "t1",
    sessionId: "s1",
    sessionName: "web-1",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    controller: null,
    subscribers: 0,
    viewers: 0,
    exited: false,
    lastOutputMsAgo: 3000,
  },
];

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
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => undefined));
  useUi.setState({ leftOpen: true, leftWidth: 260, pushToast: mocks.toast, sessions: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setViewportWidth(1024);
});

describe("FileBrowser 骨架屏", () => {
  it("pending 显示骨架行，无空态闪烁，数据到达后立即替换", async () => {
    const d = deferred<FileEntryDto[]>();
    mocks.fsList.mockReturnValue(d.promise);
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));

    await flushUntil(() => mounted!.container.querySelectorAll(".nx-skeleton-row").length > 0);
    expect(mounted!.container.querySelectorAll(".nx-skeleton-row").length).toBe(10);
    expect(mounted!.container.textContent).not.toContain("这个目录是空的");
    const status = mounted!.container.querySelector('[role="status"]');
    expect(status?.querySelector(".nx-sr-only")?.textContent).toContain("加载中");

    d.resolve(HOME_ENTRIES.map((e) => ({ ...e })));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));
    expect(mounted!.container.querySelector(".nx-skeleton-row")).toBeNull();
    expect(mounted!.container.textContent).not.toContain("这个目录是空的");
  });

  it("真空目录到达后显示空态而不是骨架", async () => {
    mocks.fsList.mockResolvedValue([]);
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
    await waitFor(() =>
      expect(mounted!.container.textContent).toContain("这个目录是空的"),
    );
    expect(mounted!.container.querySelector(".nx-skeleton-row")).toBeNull();
  });

  it("320 与 390 下骨架行低频列带隐藏类、名称条自适应", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      const d = deferred<FileEntryDto[]>();
      mocks.fsList.mockReturnValue(d.promise);
      mounted = mountWithClient(createElement(FileBrowser, { sessionId: "s1" }));
      await flushUntil(() => mounted!.container.querySelectorAll(".nx-skeleton-row").length > 0);

      const firstRow = mounted!.container.querySelector(".nx-skeleton-row");
      const bars = firstRow?.querySelectorAll(".nx-skeleton");
      expect(bars?.length).toBe(4);
      expect(bars![0].className).toContain("nx-skeleton-icon");
      expect(bars![1].className).toContain("flex-1");
      expect(bars![1].className).toContain("min-w-0");
      expect(bars![2].className).toContain("max-[560px]:hidden");
      expect(bars![3].className).toContain("max-[560px]:hidden");
      expect(bars![2].className).not.toMatch(/min-w-\[/);

      d.resolve([]);
      await waitFor(() =>
        expect(mounted!.container.textContent).toContain("这个目录是空的"),
      );
      mounted.unmount();
      mounted = undefined;
    }
  });
});

describe("FileTree 骨架屏", () => {
  it("pending 显示骨架行且不出空态，数据到达后立即替换", async () => {
    const d = deferred<FileEntryDto[]>();
    mocks.fsList.mockReturnValue(d.promise);
    mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));

    await flushUntil(() => mounted!.container.querySelectorAll(".nx-skeleton-row").length > 0);
    expect(mounted!.container.querySelectorAll(".nx-skeleton-row").length).toBe(8);
    expect(mounted!.container.textContent).not.toContain("这个目录是空的");

    d.resolve(HOME_ENTRIES.map((e) => ({ ...e })));
    await waitFor(() => expect(mounted!.container.textContent).toContain("notes.txt"));
    expect(mounted!.container.querySelector(".nx-skeleton-row")).toBeNull();
    expect(mounted!.container.textContent).not.toContain("这个目录是空的");
  });

  it("320 与 390 下骨架行名称条自适应", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      const d = deferred<FileEntryDto[]>();
      mocks.fsList.mockReturnValue(d.promise);
      mounted = mountWithClient(createElement(FileTree, { sessionId: "s1" }));
      await flushUntil(() => mounted!.container.querySelectorAll(".nx-skeleton-row").length > 0);

      const firstRow = mounted!.container.querySelector(".nx-skeleton-row");
      const bars = firstRow?.querySelectorAll(".nx-skeleton");
      expect(bars?.length).toBe(2);
      expect(bars![1].className).toContain("flex-1");
      expect(bars![1].className).toContain("min-w-0");

      d.resolve([]);
      await waitFor(() =>
        expect(mounted!.container.textContent).toContain("这个目录是空的"),
      );
      mounted.unmount();
      mounted = undefined;
    }
  });
});

describe("DockerPanel 骨架屏", () => {
  it("容器 pending 显示骨架行与概览骨架条，无空态，数据到达后立即替换", async () => {
    const ps = deferred<ContainerSummary[]>();
    const overview = deferred<{ hostStats: Record<string, number | string> }>();
    mocks.dockerPs.mockReturnValue(ps.promise);
    mocks.dockerOverview.mockReturnValue(overview.promise);
    mocks.dockerImages.mockResolvedValue(IMAGES.map((i) => ({ ...i })));
    mounted = mountWithClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));

    await flushUntil(
      () => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0,
    );
    expect(mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length).toBe(5);
    expect(mounted!.container.querySelectorAll(".nx-skeleton-chip").length).toBeGreaterThan(0);
    expect(mounted!.container.querySelector(".nx-table-empty")).toBeNull();
    expect(mounted!.container.textContent).not.toContain("这台主机上还没有容器");

    ps.resolve(CONTAINERS.map((c) => ({ ...c })));
    overview.resolve({ hostStats: { containersRunning: 1 } });
    await waitFor(() => expect(mounted!.container.textContent).toContain("web"));
    expect(mounted!.container.querySelector('tbody tr[aria-hidden="true"]')).toBeNull();
    expect(mounted!.container.querySelector(".nx-skeleton-chip")).toBeNull();
    expect(mounted!.container.textContent).not.toContain("这台主机上还没有容器");
  });

  it("镜像 tab pending 显示骨架行，数据到达后立即替换", async () => {
    const images = deferred<ImageSummary[]>();
    mocks.dockerPs.mockResolvedValue(CONTAINERS.map((c) => ({ ...c })));
    mocks.dockerOverview.mockResolvedValue({ hostStats: {} });
    mocks.dockerImages.mockReturnValue(images.promise);
    mounted = mountWithClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));

    await waitFor(() => expect(mounted!.container.textContent).toContain("web"));
    const imagesTab = [...mounted!.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.includes("镜像"),
    );
    if (!imagesTab) throw new Error("images tab not found");
    click(imagesTab);

    await flushUntil(
      () => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0,
    );
    expect(mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length).toBe(5);
    expect(mounted!.container.textContent).not.toContain("这台主机上还没有镜像");

    images.resolve(IMAGES.map((i) => ({ ...i })));
    await waitFor(() => expect(mounted!.container.textContent).toContain("nginx"));
    expect(mounted!.container.querySelector('tbody tr[aria-hidden="true"]')).toBeNull();
    expect(mounted!.container.textContent).not.toContain("这台主机上还没有镜像");
  });

  it("320 与 390 下骨架条只用百分比或自动宽度，不产生固定最小宽度", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      mocks.dockerPs.mockReturnValue(new Promise<ContainerSummary[]>(() => {}));
      mocks.dockerOverview.mockReturnValue(new Promise(() => {}));
      mocks.dockerImages.mockResolvedValue([]);
      mounted = mountWithClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));
      await flushUntil(
        () => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0,
      );
      const table = mounted!.container.querySelector("table.nx-table-fixed");
      expect(table).not.toBeNull();
      for (const bar of mounted!.container.querySelectorAll("tbody .nx-skeleton")) {
        if (bar.className.includes("nx-skeleton-icon")) continue;
        expect(bar.className).not.toMatch(/min-w-\[/);
        expect(bar.className).toMatch(/w-\d+\/\d+|ml-auto/);
      }
      mounted.unmount();
      mounted = undefined;
    }
  });
});

describe("BackgroundSessions 骨架屏", () => {
  it("pending 显示骨架行且无空态，数据到达后立即替换", async () => {
    const d = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValue(d.promise);
    mounted = mount(createElement(BackgroundSessions, { visible: true }));

    await flushUntil(
      () => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0,
    );
    expect(mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length).toBe(4);
    expect(mounted!.container.textContent).not.toContain("没有在后台运行的终端");

    d.resolve(LIVE_TABS.map((t) => ({ ...t })));
    await flushUntil(() => mounted!.container.textContent?.includes("web-1") ?? false);
    expect(mounted!.container.querySelector('tbody tr[aria-hidden="true"]')).toBeNull();
    expect(mounted!.container.textContent).not.toContain("没有在后台运行的终端");
  });

  it("真空结果到达后显示空态而不是骨架", async () => {
    mocks.listLive.mockResolvedValue([]);
    mounted = mount(createElement(BackgroundSessions, { visible: true }));
    await flushUntil(
      () => mounted!.container.textContent?.includes("没有在后台运行的终端") ?? false,
    );
    expect(mounted!.container.querySelector('tbody tr[aria-hidden="true"]')).toBeNull();
  });

  it("320 与 390 下骨架条只用百分比或自动宽度", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      mocks.listLive.mockReturnValue(new Promise<LiveTabInfo[]>(() => {}));
      mounted = mount(createElement(BackgroundSessions, { visible: true }));
      await flushUntil(
        () => mounted!.container.querySelectorAll('tbody tr[aria-hidden="true"]').length > 0,
      );
      for (const bar of mounted!.container.querySelectorAll("tbody .nx-skeleton")) {
        expect(bar.className).not.toMatch(/min-w-\[/);
        expect(bar.className).toMatch(/w-\d+\/\d+|ml-auto/);
      }
      mounted.unmount();
      mounted = undefined;
    }
  });
});
