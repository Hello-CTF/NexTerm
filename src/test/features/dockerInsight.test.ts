/** @vitest-environment jsdom */

// M61 Docker 洞察控件（overview / inspect / stats / 容器文件目录）的聚焦测试。
// mock 全部 mission-local：只替换 ../../ipc/commands、../../ipc/events、
// ../../ui/dialogs 三个边界，共享 IPC/demo/contract 文件一律不动。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  ps: vi.fn(),
  images: vi.fn(),
  inspect: vi.fn(),
  stats: vi.fn(),
  listDir: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  dockerApi: {
    overview: mocks.overview,
    ps: mocks.ps,
    images: mocks.images,
    inspect: mocks.inspect,
    stats: mocks.stats,
    containerListDir: mocks.listDir,
    action: vi.fn(),
    imageRemove: vi.fn(),
    imagePull: vi.fn(),
    logsAttach: vi.fn(),
    execAttach: vi.fn(),
  },
  terminalApi: { closeTab: vi.fn() },
}));
vi.mock("../../ipc/events", () => ({
  createBinaryChannel: vi.fn(),
  disposeChannel: vi.fn(),
  onChannelReopen: vi.fn(() => () => undefined),
}));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn() }));

import { DockerPanel } from "../../features/docker/DockerPanel";
import type { ContainerSummary } from "../../ipc/commands";
import {
  isSensitiveFileName,
  redactInspectTree,
  REDACTED_MARK,
} from "../../features/docker/dockerRedact";
import { parseStatsOutput } from "../../features/docker/statsParse";
import { useUi } from "../../app/store";

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

const inspectA = {
  Id: "aaaa1111222233334444555566667777888899990000",
  Name: "/web",
  State: { Status: "running", StartedAt: "2026-10-03T01:00:00Z", RestartCount: 2 },
  Config: {
    Image: "nginx:1.25",
    Env: ["PATH=/usr/bin", "POSTGRES_PASSWORD=hunter2secret", "TZ=UTC"],
    Labels: { "com.docker.compose.project": "shop", "x-api-token": "tok-123456" },
  },
  Mounts: [
    { Type: "volume", Name: "db-passwords", Source: "", Destination: "/secrets", RW: false },
    { Type: "bind", Name: "", Source: "/home/alice/app", Destination: "/app", RW: true },
  ],
};

const STATS_JSON_LINES = [
  '{"ID":"aaaa11112222","Name":"/web","CPUPerc":"1.50%","MemUsage":"100MiB / 2GiB","MemPerc":"4.90%","NetIO":"1kB / 2kB","BlockIO":"0B / 0B","PIDs":"12"}',
  '{"ID":"cccc55556666","Name":"/intruder","CPUPerc":"99.00%","MemUsage":"8GiB / 8GiB","MemPerc":"98.00%","NetIO":"9kB / 9kB","BlockIO":"1GB / 1GB","PIDs":"200"}',
].join("\n");

/** 轮询停止断言要真实等过两个轮询周期；包在 act 里，让期间的查询落地不触发 act 警告。 */
function sleep(ms: number): Promise<void> {
  return act(() => new Promise<void>((r) => setTimeout(r, ms)));
}

function mountPanel(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(DockerPanel, { sessionId: "s1", visible: true }),
    ),
  );
}

/** 打开第一个容器的洞察钻取（详情页签）。 */
async function openInsight(m: MountedView, index = 0): Promise<void> {
  await waitFor(() =>
    expect(m.container.querySelectorAll('button[title="详情 / 统计 / 文件"]').length).toBeGreaterThan(index),
  );
  click(m.container.querySelectorAll('button[title="详情 / 统计 / 文件"]')[index]);
  await flush();
}

describe("Docker insight controls (M61)", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: vi.fn() });
    mocks.ps.mockResolvedValue([containerA, containerB]);
    mocks.images.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({
      containers: [containerA, containerB],
      hostStats: { containersRunning: 1, containersTotal: 2, images: 5 },
    });
    mocks.inspect.mockResolvedValue(inspectA);
    mocks.stats.mockResolvedValue(STATS_JSON_LINES);
    mocks.listDir.mockResolvedValue([]);
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  // ── overview ──

  it("overview strip renders loading, then host stats chips", async () => {
    const ov = deferred<{ containers: ContainerSummary[]; hostStats: Record<string, number> }>();
    mocks.overview.mockReturnValue(ov.promise);
    const m = (mounted = mountPanel());

    await waitFor(() => expect(m.container.textContent).toContain("主机概览加载中"));
    ov.resolve({
      containers: [containerA, containerB],
      hostStats: { containersRunning: 1, containersTotal: 2, images: 5 },
    });
    // 「容器总数」只存在于概览条，等它就是等概览条完成加载
    await waitFor(() => expect(m.container.textContent).toContain("容器总数"));
    expect(m.container.textContent).toContain("运行中");
    expect(m.container.textContent).toContain("镜像");
    expect(m.container.textContent).not.toContain("主机概览加载中");
  });

  it("overview strip surfaces failure and recovers via retry", async () => {
    mocks.overview.mockRejectedValueOnce(new Error("daemon 未响应"));
    const m = (mounted = mountPanel());

    await waitFor(() => expect(m.container.textContent).toContain("主机概览加载失败"));
    expect(m.container.textContent).toContain("daemon 未响应");
    clickButton(m.container, "重试");
    await waitFor(() => expect(m.container.textContent).toContain("运行中"));
    expect(mocks.overview).toHaveBeenCalledTimes(2);
  });

  // ── inspect ──

  it("inspect renders loading, summary and redacts secrets by default", async () => {
    const pending = deferred<Record<string, unknown>>();
    mocks.inspect.mockReturnValue(pending.promise);
    const m = (mounted = mountPanel());
    await openInsight(m);

    expect(m.container.textContent).toContain("读取容器配置…");
    pending.resolve(inspectA);
    await waitFor(() => expect(m.container.textContent).toContain("环境变量"));

    // 摘要与结构照常渲染
    expect(mockedText()).toContain("nginx:1.25");
    expect(mockedText()).toContain("POSTGRES_PASSWORD");
    // 敏感值一律不上屏：env 值、敏感 label 值、敏感卷名
    expect(mockedText()).not.toContain("hunter2secret");
    expect(mockedText()).not.toContain("tok-123456");
    expect(mockedText()).not.toContain("db-passwords");
    expect(mockedText()).toContain(REDACTED_MARK);
    // 非敏感信息不受影响
    expect(mockedText()).toContain("com.docker.compose.project");
    expect(mockedText()).toContain("shop");
    expect(mockedText()).toContain("/home/alice/app");
    // 口径提示：展示级脱敏 ≠ 授权
    expect(mockedText()).toContain("所有权与强制遮蔽以内核为准");

    // 「显示敏感值」开关：显式打开后才明文
    clickButton(m.container, "显示敏感值");
    await waitFor(() => expect(mockedText()).toContain("hunter2secret"));
    expect(mockedText()).toContain("tok-123456");
    expect(mockedText()).toContain("db-passwords");
  });

  it("inspect failure keeps the view open and retries in place", async () => {
    mocks.inspect.mockRejectedValueOnce(new Error("inspect 失败: no such container"));
    const m = (mounted = mountPanel());
    await openInsight(m);

    await waitFor(() => expect(mockedText()).toContain("inspect 失败: no such container"));
    expect(mockedText()).toContain("web");
    clickButton(m.container, "重试");
    await waitFor(() => expect(mockedText()).toContain("环境变量"));
  });

  it("drops a stale inspect response after switching containers", async () => {
    const stale = deferred<Record<string, unknown>>();
    mocks.inspect.mockImplementation((_s: string, containerId: string) => {
      if (containerId === containerA.id) return stale.promise;
      return Promise.resolve({
        Id: containerB.id,
        Name: "/worker",
        State: { Status: "exited" },
        Config: { Image: "redis:7", Env: [], Labels: {} },
        Mounts: [],
      });
    });
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    expect(mockedText()).toContain("读取容器配置…");

    // 容器 A 的响应还在飞：返回列表、改开容器 B
    clickButton(m.container, "返回");
    await waitFor(() => expect(m.container.querySelectorAll('button[title="详情 / 统计 / 文件"]').length).toBe(2));
    await openInsight(m, 1);
    await waitFor(() => expect(mockedText()).toContain("redis:7"));

    // A 的迟到响应落地：不得渲染到 B 的界面上
    stale.resolve({ ...inspectA, Name: "/web-stale", Config: { Image: "nginx:1.25", Env: ["LEAK=AAA_SECRET_VALUE"], Labels: {} } });
    await flush();
    expect(mockedText()).not.toContain("AAA_SECRET_VALUE");
    expect(mockedText()).not.toContain("web-stale");
    expect(mockedText()).toContain("redis:7");
  });

  // ── stats ──

  it("stats tab renders rows for this session only and marks the current container", async () => {
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "统计");

    await waitFor(() => expect(mockedText()).toContain("3s 轮询"));
    expect(mockedText()).toContain("web");
    expect(mockedText()).toContain("1.50%");
    expect(mockedText()).toContain("当前");
    // 归属比对：不属于当前会话容器清单的行（后端多返回的 intruder）不上屏
    expect(mockedText()).not.toContain("intruder");
    expect(mockedText()).not.toContain("98.00%");
  });

  it("stats empty state explains when the current container is stopped", async () => {
    mocks.stats.mockResolvedValue("");
    const m = (mounted = mountPanel());
    await openInsight(m, 1); // worker 已停止
    clickButton(m.container, "统计");

    await waitFor(() => expect(mockedText()).toContain("当前容器已停止"));
    expect(mockedText()).toContain("只有运行中的容器才有统计");
  });

  it("stats polling stops after leaving the stats tab", async () => {
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "统计");
    await waitFor(() => expect(mocks.stats).toHaveBeenCalledTimes(1));

    clickButton(m.container, "文件");
    await waitFor(() => expect(mockedText()).toContain("仅列出容器内目录"));
    const callsAfterLeave = mocks.stats.mock.calls.length;
    await sleep(3600);
    expect(mocks.stats.mock.calls.length).toBe(callsAfterLeave);
  }, 15000);

  it("stats polling stops on unmount", async () => {
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "统计");
    await waitFor(() => expect(mocks.stats).toHaveBeenCalledTimes(1));

    m.unmount();
    mounted = undefined;
    await sleep(3600);
    expect(mocks.stats).toHaveBeenCalledTimes(1);
  }, 15000);

  // ── files ──

  it("files tab lists entries without dot-link noise and navigates directories", async () => {
    mocks.listDir.mockImplementation((_s: string, _c: string, path: string) => {
      if (path === "/") return Promise.resolve([".", "..", "app/", "etc/", "server.js", ".env"]);
      if (path === "/etc") return Promise.resolve(["nginx/", "passwd"]);
      return Promise.resolve([]);
    });
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "文件");

    await waitFor(() => expect(mockedText()).toContain("server.js"));
    const rows = m.container.querySelectorAll("tbody tr");
    // `.` / `..` 被过滤，其余 4 条上屏
    expect(rows.length).toBe(4);
    expect(mockedText()).not.toContain("etc/nginx");
    // 敏感文件名只打标，不读内容
    expect(mockedText()).toContain("敏感");
    expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/");

    // 进入 etc/
    const etcRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("etc"),
    );
    if (!etcRow) throw new Error("etc row not found");
    click(etcRow);
    await waitFor(() => expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/etc"));
    await waitFor(() => expect(mockedText()).toContain("passwd"));
  });

  it("files tab shows inaccessible path errors with retry and a way back", async () => {
    mocks.listDir.mockImplementation((_s: string, _c: string, path: string) => {
      if (path === "/") return Promise.resolve(["etc/"]);
      if (path === "/etc") return Promise.resolve(["shadow"]);
      return Promise.reject(new Error("docker ls exited with code 1: Permission denied"));
    });
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "文件");
    await waitFor(() => expect(mockedText()).toContain("etc"));

    const etcRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("etc"),
    );
    if (!etcRow) throw new Error("etc row not found");
    click(etcRow);
    await waitFor(() => expect(mockedText()).toContain("shadow"));

    // shadow 不是目录：后端报错，原地给重试 / 返回上级
    const shadowRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("shadow"),
    );
    if (!shadowRow) throw new Error("shadow row not found");
    click(shadowRow);
    await waitFor(() => expect(mockedText()).toContain("Permission denied"));
    expect(mockedText()).toContain("重试");

    clickButton(m.container, "返回上级");
    await waitFor(() => expect(mockedText()).toContain("shadow"));
    expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/etc");
  });

  it("files tab renders an empty state for empty directories", async () => {
    mocks.listDir.mockResolvedValue([".", ".."]);
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "文件");
    await waitFor(() => expect(mockedText()).toContain("这个目录是空的"));
  });

  function mockedText(): string {
    return mounted?.container.textContent ?? "";
  }
});

describe("dockerRedact helpers", () => {
  it("masks sensitive env entries and label values but keeps structure", () => {
    const input = {
      Config: {
        Env: ["PATH=/usr/bin", "API_SECRET=s3cr3t", "NO_VALUE"],
        Labels: { "com.example.ok": "fine", "auth.token": "t0k" },
      },
      Nested: { DbPassword: "p@ss" },
      Mounts: [
        { Type: "volume", Name: "db-passwords", Destination: "/secrets" },
        { Type: "bind", Name: "", Source: "/home/alice/app" },
      ],
    };
    const out = redactInspectTree(input, false) as typeof input;
    expect(out.Config.Env[0]).toBe("PATH=/usr/bin");
    expect(out.Config.Env[1]).toBe(`API_SECRET=${REDACTED_MARK}`);
    expect(out.Config.Env[2]).toBe("NO_VALUE");
    expect(out.Config.Labels["com.example.ok"]).toBe("fine");
    expect(out.Config.Labels["auth.token"]).toBe(REDACTED_MARK);
    expect(out.Nested.DbPassword).toBe(REDACTED_MARK);
    // 卷名按值判敏感；源路径保留（排查挂载必须看到源）
    expect(out.Mounts[0].Name).toBe(REDACTED_MARK);
    expect(out.Mounts[0].Destination).toBe("/secrets");
    expect(out.Mounts[1].Source).toBe("/home/alice/app");
    // reveal 时原样返回
    expect(redactInspectTree(input, true)).toEqual(input);
  });

  it("flags sensitive-looking container file names", () => {
    expect(isSensitiveFileName(".env")).toBe(true);
    expect(isSensitiveFileName(".env.production")).toBe(true);
    expect(isSensitiveFileName("id_rsa")).toBe(true);
    expect(isSensitiveFileName("cert.pem")).toBe(true);
    expect(isSensitiveFileName("app_secret.yaml")).toBe(true);
    expect(isSensitiveFileName("server.js")).toBe(false);
    expect(isSensitiveFileName("passwd")).toBe(true);
  });
});

describe("parseStatsOutput", () => {
  it("parses docker JSON-lines stats (Go SDK / CLI fallback contract)", () => {
    const rows = parseStatsOutput(STATS_JSON_LINES);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      id: "aaaa11112222",
      name: "web",
      cpuPerc: "1.50%",
      memUsage: "100MiB / 2GiB",
      pids: "12",
    });
  });

  it("parses demo-mode pipe rows and skips unparseable lines", () => {
    const rows = parseStatsOutput("aaaa11112222|web|0.50%|64MiB\r\njunk-line\n");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ id: "aaaa11112222", name: "web", cpuPerc: "0.50%", memUsage: "64MiB" });
  });
});
