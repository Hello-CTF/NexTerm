/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  schemas: vi.fn(),
  tables: vi.fn(),
  query: vi.fn(),
  redisScan: vi.fn(),
  redisInspect: vi.fn(),
  redisCommand: vi.fn(),
  overview: vi.fn(),
  ps: vi.fn(),
  images: vi.fn(),
  inspect: vi.fn(),
  stats: vi.fn(),
  listDir: vi.fn(),
  imagePull: vi.fn(),
  env: vi.fn(),
  list: vi.fn(),
  create: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dbApi: {
      schemas: mocks.schemas,
      tables: mocks.tables,
      query: mocks.query,
      columns: vi.fn(),
      redisScan: mocks.redisScan,
      redisInspect: mocks.redisInspect,
      redisCommand: mocks.redisCommand,
      redisSetTtl: vi.fn(),
    },
    dockerApi: {
      overview: mocks.overview,
      ps: mocks.ps,
      images: mocks.images,
      inspect: mocks.inspect,
      stats: mocks.stats,
      containerListDir: mocks.listDir,
      action: vi.fn(),
      imageRemove: vi.fn(),
      imagePull: mocks.imagePull,
      logsAttach: vi.fn(),
      execAttach: vi.fn(),
    },
    forwardApi: {
      env: mocks.env,
      list: mocks.list,
      create: mocks.create,
      createSocks: vi.fn(),
      remove: vi.fn(),
    },
    sessionApi: {},
    terminalApi: { closeTab: vi.fn() },
    vaultApi: {},
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createBinaryChannel: vi.fn(),
    disposeChannel: vi.fn(),
    onChannelReopen: vi.fn(() => () => undefined),
  };
});

import { DbPanel } from "../../features/db/DbPanel";
import { DockerPanel } from "../../features/docker/DockerPanel";
import { ContainerInsight } from "../../features/docker/ContainerInsight";
import { ForwardPanel } from "../../features/forward/ForwardPanel";
import { useUi } from "../../app/store";
import type { ContainerSummary } from "../../ipc/commands";

const STATS_JSON_LINES = [
  '{"ID":"aaaa11112222","Name":"/web","CPUPerc":"1.50%","MemUsage":"100MiB / 2GiB","MemPerc":"4.90%","NetIO":"1kB / 2kB","BlockIO":"0B / 0B","PIDs":"12"}',
].join("\n");

const containerA: ContainerSummary = {
  id: "aaaa11112222",
  name: "web",
  image: "nginx:1.25",
  state: "running",
  status: "Up 2 hours",
  ports: "80/tcp",
  composeProject: null,
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.schemas.mockResolvedValue(["mydb"]);
  mocks.tables.mockResolvedValue([]);
  mocks.query.mockResolvedValue({
    columns: [],
    rows: [],
    rowsAffected: 0,
    durationMs: 0,
    truncated: false,
    error: null,
  });
  mocks.redisScan.mockResolvedValue([0, ["k1"]]);
  mocks.redisInspect.mockResolvedValue({ key: "k1", keyType: "string", ttl: -1, value: "v" });
  mocks.redisCommand.mockResolvedValue("OK");
  mocks.overview.mockResolvedValue({ containers: [containerA], hostStats: {} });
  mocks.ps.mockResolvedValue([containerA]);
  mocks.images.mockResolvedValue([]);
  mocks.inspect.mockResolvedValue({ Id: "aaaa", Name: "/web", State: {}, Config: {}, Mounts: [] });
  mocks.stats.mockResolvedValue(STATS_JSON_LINES);
  mocks.listDir.mockResolvedValue([]);
  mocks.imagePull.mockResolvedValue(undefined);
  mocks.env.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.list.mockResolvedValue([]);
  mocks.create.mockResolvedValue({
    id: "f1",
    sessionId: "s",
    listenPort: 13306,
    targetHost: "127.0.0.1",
    targetPort: 3306,
    kind: "local",
    createdAt: 1,
  });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): void {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
}

function withQueryClient(node: React.ReactNode): React.ReactElement {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return createElement(QueryClientProvider, { client }, node);
}

describe("Enter 提交的 IME 守卫", () => {
  it("Redis 匹配模式：组合中 Enter 不触发 SCAN，普通 Enter 触发", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => mocks.redisScan.mock.calls.length > 0);
    const callsAfterLoad = mocks.redisScan.mock.calls.length;
    const input = mounted.container.querySelector<HTMLInputElement>('[aria-label="键匹配模式"]');
    if (!input) throw new Error("pattern input not found");
    keyDown(input, "Enter", { isComposing: true });
    keyDown(input, "Enter", { keyCode: 229 } as KeyboardEventInit);
    expect(mocks.redisScan).toHaveBeenCalledTimes(callsAfterLoad);
    keyDown(input, "Enter");
    expect(mocks.redisScan).toHaveBeenCalledTimes(callsAfterLoad + 1);
  });

  it("Redis 命令台：组合中 Enter 不执行，普通 Enter 执行", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => mocks.redisScan.mock.calls.length > 0);
    const cmdInput = mounted.container.querySelector<HTMLInputElement>(
      'input[aria-label="Redis 命令"]',
    );
    if (!cmdInput) throw new Error("redis command input not found");
    keyDown(cmdInput, "Enter", { isComposing: true });
    expect(mocks.redisCommand).not.toHaveBeenCalled();
    keyDown(cmdInput, "Enter");
    await flushUntil(() => mocks.redisCommand.mock.calls.length > 0);
    expect(mocks.redisCommand).toHaveBeenCalledWith("c1", ["INFO", "memory"]);
  });

  it("MySQL 编辑器：组合中 Ctrl+Enter 不执行，普通 Ctrl+Enter 执行", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mocks.schemas.mock.calls.length > 0);
    const host = mounted.container.querySelector<HTMLElement>(".bg-term");
    if (!host) throw new Error("editor host not found");
    keyDown(host, "Enter", { ctrlKey: true, isComposing: true });
    expect(mocks.query).not.toHaveBeenCalled();
    keyDown(host, "Enter", { ctrlKey: true });
    await flushUntil(() => mocks.query.mock.calls.length > 0);
  });

  it("Docker 拉取输入：组合中 Enter 不拉取，普通 Enter 拉取", async () => {
    mounted = mount(withQueryClient(createElement(DockerPanel, { sessionId: "s" })));
    await flushUntil(() => mocks.ps.mock.calls.length > 0);
    const input = mounted.container.querySelector<HTMLInputElement>('[aria-label="拉取镜像名称"]');
    if (!input) throw new Error("pull input not found");
    setInputValue(input, "nginx:alpine");
    keyDown(input, "Enter", { isComposing: true });
    expect(mocks.imagePull).not.toHaveBeenCalled();
    keyDown(input, "Enter");
    await flushUntil(() => mocks.imagePull.mock.calls.length > 0);
  });

  it("转发目标端口：组合中 Enter 不创建，普通 Enter 创建", async () => {
    mounted = mount(withQueryClient(createElement(ForwardPanel, { sessionId: "s" })));
    await flushUntil(() => mocks.env.mock.calls.length > 0);
    const input = mounted.container.querySelector<HTMLInputElement>('[aria-label="目标端口"]');
    if (!input) throw new Error("target port input not found");
    keyDown(input, "Enter", { isComposing: true });
    expect(mocks.create).not.toHaveBeenCalled();
    keyDown(input, "Enter");
    await flushUntil(() => mocks.create.mock.calls.length > 0);
  });
});

describe("小屏响应式结构契约", () => {
  it("MySQL：运行按钮 sticky-right，编辑器可收缩，结果区有最小高度", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => mocks.schemas.mock.calls.length > 0);
    const runButton = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.includes("运行") && b.className.includes("nx-btn-primary"),
    );
    expect(runButton?.className).toContain("sticky");
    expect(runButton?.className).toContain("right-0");
    const editorHost = mounted.container.querySelector(".bg-term");
    expect(editorHost?.className).toContain("max-h-[45%]");
    expect(editorHost?.className).toContain(" shrink");
    expect(editorHost?.className).not.toContain("shrink-0");
    const resultRegion = mounted.container.querySelector(".min-h-\\[48px\\]");
    expect(resultRegion).toBeTruthy();
  });

  it("Redis：窄屏堆叠布局类存在，宽屏恢复 272px 侧栏", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => mocks.redisScan.mock.calls.length > 0);
    const pane = mounted.container.querySelector(".nx-pane");
    expect(pane?.className).toContain("flex-col");
    expect(pane?.className).toContain("min-[560px]:flex-row");
    const sideColumn = pane?.firstElementChild;
    expect(sideColumn?.className).toContain("w-full");
    expect(sideColumn?.className).toContain("max-h-[45%]");
    expect(sideColumn?.className).toContain("min-[560px]:w-[272px]");
  });

  it("Docker 容器表：操作列 sticky-right，触控目标放大，次要 hint 小屏隐藏", async () => {
    mounted = mount(withQueryClient(createElement(DockerPanel, { sessionId: "s" })));
    await flushUntil(() => Boolean(mounted!.container.querySelector("tbody td.sticky")));
    const table = mounted.container.querySelector("table");
    const lastTh = table?.querySelector("thead th:last-child");
    expect(lastTh?.className).toContain("right-0");
    const lastTd = table?.querySelector("tbody tr td:last-child");
    expect(lastTd?.className).toContain("sticky");
    expect(lastTd?.className).toContain("right-0");
    expect(lastTd?.className).toContain("bg-[var(--nx-bg-pane)]");
    const actionButtons = table?.querySelectorAll("tbody .nx-icon-btn-sm");
    expect(actionButtons?.length).toBeGreaterThan(0);
    for (const button of actionButtons ?? []) {
      expect(button.className).toContain("pointer-coarse:h-7");
      expect(button.className).toContain("pointer-coarse:w-7");
    }
    const checkboxes = table?.querySelectorAll(".nx-check");
    expect(checkboxes?.length).toBeGreaterThan(0);
    for (const box of checkboxes ?? []) {
      expect(box.className).toContain("pointer-coarse:size-6");
    }
    const hiddenHints = [...mounted.container.querySelectorAll(".nx-toolbar .nx-hint")].filter(
      (el) => el.className.includes("hidden"),
    );
    expect(hiddenHints.length).toBeGreaterThanOrEqual(2);
  });

  it("Docker 镜像表：删除操作列 sticky-right", async () => {
    mocks.images.mockResolvedValue([
      {
        id: "sha256:abc",
        repository: "nginx",
        tag: "1.25",
        size: "100MB",
        createdSince: "2 weeks ago",
      },
    ]);
    mounted = mount(withQueryClient(createElement(DockerPanel, { sessionId: "s" })));
    await flushUntil(() => mocks.images.mock.calls.length > 0);
    const imagesTab = [...mounted.container.querySelectorAll(".nx-segment button")].find((b) =>
      b.textContent?.includes("镜像"),
    );
    if (!imagesTab) throw new Error("images tab not found");
    click(imagesTab);
    await flushUntil(() => Boolean(mounted!.container.querySelector("tbody td.sticky")));
    const lastTd = mounted.container.querySelector("tbody tr td:last-child");
    expect(lastTd?.className).toContain("sticky");
    expect(lastTd?.className).toContain("right-0");
  });

  it("ContainerInsight：工具栏可换行，统计表容器列 sticky-left 且有最小宽度", async () => {
    mounted = mount(
      withQueryClient(
        createElement(ContainerInsight, {
          sessionId: "s",
          container: containerA,
          visible: true,
          onClose: () => undefined,
        }),
      ),
    );
    const toolbar = mounted.container.querySelector(".nx-toolbar");
    expect(toolbar?.className).toContain("flex-wrap");
    clickButton(mounted.container, "统计");
    await flushUntil(() => Boolean(mounted!.container.querySelector("tbody td.sticky")));
    const table = mounted.container.querySelector("table");
    expect(table?.className).toContain("min-w-[620px]");
    const firstTh = table?.querySelector("thead th:first-child");
    expect(firstTh?.className).toContain("left-0");
    expect(firstTh?.className).toContain("z-[2]");
    const firstTd = table?.querySelector("tbody td:first-child");
    expect(firstTd?.className).toContain("sticky");
    expect(firstTd?.className).toContain("left-0");
    const metricHeaders = [...(table?.querySelectorAll("thead th") ?? [])].slice(1);
    expect(metricHeaders.length).toBe(6);
    for (const th of metricHeaders) {
      expect(th.getAttribute("style") ?? "").not.toContain("width");
    }
  });

  it("转发列表：停止操作列 sticky-right", async () => {
    mocks.list.mockResolvedValue([
      {
        id: "f1",
        sessionId: "s",
        listenPort: 13306,
        targetHost: "127.0.0.1",
        targetPort: 3306,
        kind: "local",
        createdAt: 1,
      },
    ]);
    mounted = mount(withQueryClient(createElement(ForwardPanel, { sessionId: "s" })));
    await flushUntil(() => Boolean(mounted!.container.querySelector("tbody td.sticky")));
    const lastTh = mounted.container.querySelector("thead th:last-child");
    expect(lastTh?.className).toContain("right-0");
    const lastTd = mounted.container.querySelector("tbody tr td:last-child");
    expect(lastTd?.className).toContain("sticky");
    expect(lastTd?.className).toContain("right-0");
  });
});
