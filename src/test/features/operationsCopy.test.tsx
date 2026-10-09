/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  flushUntil,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    overview: vi.fn(),
    ps: vi.fn(),
    images: vi.fn(),
    action: vi.fn(),
    imagePull: vi.fn(),
    logsAttach: vi.fn(),
    schemas: vi.fn(),
    tables: vi.fn(),
    query: vi.fn(),
    redisScan: vi.fn(),
    env: vi.fn(),
    list: vi.fn(),
    create: vi.fn(),
    auditQuery: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      overview: mocks.overview,
      ps: mocks.ps,
      images: mocks.images,
      inspect: vi.fn(),
      stats: vi.fn(),
      containerListDir: vi.fn(),
      action: mocks.action,
      imageRemove: vi.fn(),
      imagePull: mocks.imagePull,
      logsAttach: mocks.logsAttach,
      execAttach: vi.fn(),
    },
    dbApi: {
      schemas: mocks.schemas,
      tables: mocks.tables,
      query: mocks.query,
      columns: vi.fn(),
      redisScan: mocks.redisScan,
      redisInspect: vi.fn(),
      redisCommand: vi.fn(),
      redisSetTtl: vi.fn(),
    },
    forwardApi: {
      env: mocks.env,
      list: mocks.list,
      create: mocks.create,
      createSocks: vi.fn(),
      remove: vi.fn(),
    },
    assetApi: { auditQuery: mocks.auditQuery },
    terminalApi: { closeTab: vi.fn() },
    sessionApi: {},
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
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { DockerPanel } from "../../features/docker/DockerPanel";
import { DbPanel } from "../../features/db/DbPanel";
import { ForwardPanel } from "../../features/forward/ForwardPanel";
import { AuditView } from "../../features/settings/AuditView";
import { useUi } from "../../app/store";
import type { ContainerSummary } from "../../ipc/commands";

const containerA: ContainerSummary = {
  id: "aaaa11112222",
  name: "web",
  image: "nginx:1.25",
  state: "running",
  status: "Up 2 hours",
  ports: "80/tcp",
  composeProject: null,
};
const containerB: ContainerSummary = {
  id: "bbbb33334444",
  name: "worker",
  image: "redis:7",
  state: "exited",
  status: "Exited (0) 1 hour ago",
  ports: "",
  composeProject: null,
};

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
  mocks.overview.mockResolvedValue({ containers: [], hostStats: {} });
  mocks.ps.mockResolvedValue([containerA, containerB]);
  mocks.images.mockResolvedValue([]);
  mocks.action.mockResolvedValue(undefined);
  mocks.imagePull.mockResolvedValue("");
  mocks.logsAttach.mockRejectedValue(new Error("daemon 未响应"));
  mocks.schemas.mockResolvedValue(["mydb"]);
  mocks.tables.mockResolvedValue([]);
  mocks.query.mockResolvedValue({
    columns: [],
    rows: [],
    rowsAffected: 3,
    durationMs: 1,
    truncated: false,
    error: null,
  });
  mocks.redisScan.mockResolvedValue([0, []]);
  mocks.env.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.list.mockResolvedValue([
    {
      id: "f1",
      sessionId: "s",
      listenPort: 13306,
      targetHost: "10.0.0.8",
      targetPort: 3306,
      kind: "local",
      createdAt: 1,
    },
    {
      id: "f2",
      sessionId: "s",
      listenPort: 1080,
      targetHost: null,
      targetPort: null,
      kind: "socks",
      createdAt: 2,
    },
  ]);
  mocks.create.mockResolvedValue({ id: "f3", kind: "local" });
  mocks.auditQuery.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function text(): string {
  return mounted?.container.textContent ?? "";
}

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

describe("DockerPanel 文案", () => {
  it("容器操作 toast 用中文动作，失败用全角冒号", async () => {
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));
    await flushUntil(() => text().includes("web"));

    click(mounted.container.querySelector('button[title="重启"]')!);
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", "已重启 web"),
    );

    mocks.action.mockRejectedValueOnce(new Error("daemon 未响应"));
    click(mounted.container.querySelector('button[title="停止"]')!);
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", "停止 web 失败：daemon 未响应"),
    );
  });

  it("状态徽章中文化，旁边保留 Docker 原始 status", async () => {
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));
    await flushUntil(() => text().includes("worker"));
    expect(text()).toContain("运行中");
    expect(text()).toContain("已停止");
    expect(text()).toContain("Up 2 hours");
    expect(text()).toContain("Exited (0) 1 hour ago");
    expect(text()).not.toContain("exit 0 · 已停止");
  });

  it("工具栏标注真实刷新节奏，镜像空态与容器一致", async () => {
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));
    await flushUntil(() => text().includes("容器 5s · 镜像与主机概览 30s 自动刷新"));
    const segment = [...mounted.container.querySelectorAll(".nx-segment-item")].find((b) =>
      b.textContent?.includes("镜像"),
    );
    click(segment!);
    await flushUntil(() => text().includes("这台主机上还没有镜像"));
    expect(text()).not.toContain("本机没有镜像");
  });

  it("拉取镜像提示成句，失败用全角冒号", async () => {
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));
    await flushUntil(() => text().includes("web"));
    const input = mounted.container.querySelector<HTMLInputElement>('[aria-label="拉取镜像名称"]')!;
    setInputValue(input, "nginx:alpine");
    clickButton(mounted.container, "拉取");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("info", "正在拉取 nginx:alpine（最长 10 分钟）…"),
    );
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("success", "nginx:alpine 拉取完成"),
    );

    setInputValue(input, "redis:7");
    mocks.imagePull.mockRejectedValueOnce(new Error("registry 超时"));
    clickButton(mounted.container, "拉取");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", "拉取失败：registry 超时"),
    );
  });

  it("日志附加失败提示中文化并用全角冒号", async () => {
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1", visible: true }));
    await flushUntil(() => text().includes("web"));
    click(mounted.container.querySelector('button[title="查看日志"]')!);
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith("error", "读取日志失败：daemon 未响应"),
    );
  });
});

describe("DbPanel 文案", () => {
  it("SQL 运行术语统一为「运行」", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "mysql" }));
    await flushUntil(() => text().includes("mydb"));
    expect(text()).toContain("点右上角「运行」");
    const run = mounted.container.querySelector<HTMLButtonElement>(
      'button[title="运行整段 SQL，或只运行选中部分（一次只执行一条语句）"]',
    );
    expect(run).not.toBeNull();
    expect(run!.textContent).toContain("运行");
    click(run!);
    await flushUntil(() => text().includes("运行成功"));
    expect(text()).toContain("3 行受影响");
    expect(text()).not.toContain("执行成功");
  });

  it("Redis 命令台说明与真实行为一致，不引用内部章节号", async () => {
    mounted = mount(createElement(DbPanel, { connId: "c1", kind: "redis" }));
    await flushUntil(() => text().includes("命令台"));
    const bar = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("命令台"),
    );
    if (!bar) throw new Error("命令台折叠条未找到");
    click(bar);
    await flushUntil(() => text().includes("命令台 · FLUSHALL"));
    expect(text()).toContain("命令台 · FLUSHALL / FLUSHDB / DEL / UNLINK 会删除数据，SHUTDOWN 会停止服务，CONFIG / DEBUG / EVAL / EVALSHA / FCALL 可改动服务或执行任意脚本；这些命令执行前会要求确认，其余命令立即执行");
    expect(text()).not.toContain("§");
    expect(text()).not.toContain("M3");
  });
});

describe("ForwardPanel 文案", () => {
  it("转发类型徽章与表单按钮叫法一致", async () => {
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s" }));
    await flushUntil(() => text().includes("10.0.0.8"));
    expect(text()).toContain("静态转发");
    expect(text()).toContain("SOCKS5 代理");
    expect(text()).not.toContain("本地静态");
  });

  it("本机监听的成功 toast 标明仅本机可访问", async () => {
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s" }));
    await flushUntil(() => text().includes("10.0.0.8"));
    clickButton(mounted.container, "创建");
    await waitFor(() =>
      expect(mocks.toast).toHaveBeenCalledWith(
        "success",
        "转发已就绪 → 127.0.0.1:13306（仅本机可访问）",
      ),
    );
  });
});

describe("AuditView 文案", () => {
  it("页脚只承诺来源与有退出码时展示，不承诺齐全，不引用内部章节号", async () => {
    mocks.auditQuery.mockResolvedValue([
      {
        id: 1,
        ts: 1,
        sessionId: "s1",
        assetId: null,
        source: "user",
        kind: "exec",
        payload: { cmd: "ls" },
        exitCode: 0,
        durationMs: 12,
      },
      {
        id: 2,
        ts: 2,
        sessionId: null,
        assetId: null,
        source: "ai",
        kind: "file.write",
        payload: { path: "/tmp/x" },
        exitCode: null,
        durationMs: null,
      },
    ]);
    mounted = mount(createElement(AuditView));
    await flushUntil(() => text().includes("✓ 0"));
    expect(text()).toContain("会话命令、AI 动作、文件写操作与设备上下线都会落库，逐条标注来源；有退出码的记录会一并展示。");
    expect(text()).not.toContain("齐全");
    expect(text()).not.toContain("可导出");
    expect(text()).not.toContain("§");
    const rows = [...mounted.container.querySelectorAll("tbody tr")];
    expect(rows[0].textContent).toContain("✓ 0");
    expect(rows[1].textContent).toContain("—");
  });
});
