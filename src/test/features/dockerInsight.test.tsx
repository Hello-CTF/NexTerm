/** @vitest-environment jsdom */

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
  action: vi.fn(),
  imageRemove: vi.fn(),
  logsAttach: vi.fn(),
  reopenCbs: [] as (() => void)[],
  ask: vi.fn(),
  realAsk: null as null | ((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) => Promise<boolean>),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      overview: mocks.overview,
      ps: mocks.ps,
      images: mocks.images,
      inspect: mocks.inspect,
      stats: mocks.stats,
      containerListDir: mocks.listDir,
      action: mocks.action,
      imageRemove: mocks.imageRemove,
      imagePull: vi.fn(),
      logsAttach: mocks.logsAttach,
      execAttach: vi.fn(),
    },
    terminalApi: { closeTab: vi.fn(() => Promise.resolve()) },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createBinaryChannel: vi.fn(),
    disposeChannel: vi.fn(),
    onChannelReopen: (_ch: unknown, cb: () => void) => {
      mocks.reopenCbs.push(cb);
      return () => undefined;
    },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  mocks.realAsk = actual.ask;
  return { ...actual, ask: mocks.ask };
});

import { DockerPanel } from "../../features/docker/DockerPanel";
import type { ContainerSummary, ImageSummary } from "../../ipc/commands";
import { dialogLevelForKind } from "../../app/App";
import { registerDialogHandlers } from "../../ui/dialogs";
import { DialogHost } from "../../ui/DialogHost";
import {
  buildInspectViewModel,
  isSensitiveFileName,
  isSensitiveName,
  redactBindEntry,
  redactContainerPath,
  redactFileName,
  redactInspectTree,
  redactMountSource,
  redactPathInText,
  redactUrlCredentials,
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
    {
      Type: "volume",
      Name: "db-passwords",
      Source: "/var/lib/docker/volumes/db-passwords/_data",
      Destination: "/secrets",
      RW: false,
    },
    { Type: "bind", Name: "", Source: "/home/alice/app", Destination: "/app", RW: true },
  ],
  HostConfig: { Binds: ["db-passwords:/secrets", "/home/alice/app:/app:ro"] },
};

const STATS_JSON_LINES = [
  '{"ID":"aaaa11112222","Name":"/web","CPUPerc":"1.50%","MemUsage":"100MiB / 2GiB","MemPerc":"4.90%","NetIO":"1kB / 2kB","BlockIO":"0B / 0B","PIDs":"12"}',
  '{"ID":"cccc55556666","Name":"/intruder","CPUPerc":"99.00%","MemUsage":"8GiB / 8GiB","MemPerc":"98.00%","NetIO":"9kB / 9kB","BlockIO":"1GB / 1GB","PIDs":"200"}',
].join("\n");

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
    mocks.reopenCbs.length = 0;
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

  it("overview strip renders loading, then host stats chips", async () => {
    const ov = deferred<{ containers: ContainerSummary[]; hostStats: Record<string, number> }>();
    mocks.overview.mockReturnValue(ov.promise);
    const m = (mounted = mountPanel());

    await waitFor(() => expect(m.container.textContent).toContain("主机概览加载中"));
    ov.resolve({
      containers: [containerA, containerB],
      hostStats: { containersRunning: 1, containersTotal: 2, images: 5 },
    });
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

  it("inspect renders loading, summary and keeps secrets out of the DOM entirely", async () => {
    const pending = deferred<Record<string, unknown>>();
    mocks.inspect.mockReturnValue(pending.promise);
    const m = (mounted = mountPanel());
    await openInsight(m);

    expect(m.container.textContent).toContain("读取容器配置…");
    pending.resolve(inspectA);
    await waitFor(() => expect(m.container.textContent).toContain("环境变量"));

    expect(mockedText()).toContain("nginx:1.25");
    expect(mockedText()).toContain("POSTGRES_PASSWORD");
    expect(mockedText()).not.toContain("hunter2secret");
    expect(mockedText()).not.toContain("tok-123456");
    expect(mockedText()).not.toContain("db-passwords");
    expect(mockedText()).toContain(REDACTED_MARK);
    expect(mockedText()).toContain("/var/lib/docker/volumes/");
    expect(mockedText()).toContain("com.docker.compose.project");
    expect(mockedText()).toContain("shop");
    expect(mockedText()).toContain("/home/alice/app");
    expect(mockedText()).toContain("敏感值已遮蔽 · 按名称和连接串内嵌凭据自动识别");
    expect(mockedText()).not.toContain("显示敏感值");

    const rowOf = (needle: string) =>
      [...m.container.querySelectorAll("tbody tr")].find((r) => r.textContent?.includes(needle));
    const bindRow = rowOf("/home/alice/app");
    const volumeRow = rowOf("/var/lib/docker/volumes/");
    if (!bindRow || !volumeRow) throw new Error("mount rows not found");
    expect(bindRow.textContent).toContain("RW");
    expect(bindRow.textContent).not.toContain("RO");
    expect(volumeRow.textContent).toContain("RO");
    expect(volumeRow.textContent).not.toContain("RW");

    clickButton(m.container, "刷新");
    clickButton(m.container, "统计");
    await waitFor(() => expect(mockedText()).toContain("每 3 秒自动刷新"));
    clickButton(m.container, "文件");
    await waitFor(() => expect(mockedText()).toContain("仅列出容器内目录"));
    clickButton(m.container, "详情");
    await waitFor(() => expect(mockedText()).toContain("环境变量"));
    expect(mockedText()).not.toContain("hunter2secret");
    expect(mockedText()).not.toContain("tok-123456");
    expect(mockedText()).not.toContain("db-passwords");
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

    clickButton(m.container, "返回");
    await waitFor(() => expect(m.container.querySelectorAll('button[title="详情 / 统计 / 文件"]').length).toBe(2));
    await openInsight(m, 1);
    await waitFor(() => expect(mockedText()).toContain("redis:7"));

    stale.resolve({ ...inspectA, Name: "/web-stale", Config: { Image: "nginx:1.25", Env: ["LEAK=AAA_SECRET_VALUE"], Labels: {} } });
    await flush();
    expect(mockedText()).not.toContain("AAA_SECRET_VALUE");
    expect(mockedText()).not.toContain("web-stale");
    expect(mockedText()).toContain("redis:7");
  });

  it("stats tab renders rows for this session only and marks the current container", async () => {
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "统计");

    await waitFor(() => expect(mockedText()).toContain("每 3 秒自动刷新"));
    expect(mockedText()).toContain("web");
    expect(mockedText()).toContain("1.50%");
    expect(mockedText()).toContain("当前");
    expect(mockedText()).not.toContain("intruder");
    expect(mockedText()).not.toContain("98.00%");
  });

  it("stats empty state explains when the current container is stopped", async () => {
    mocks.stats.mockResolvedValue("");
    const m = (mounted = mountPanel());
    await openInsight(m, 1);
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
    expect(rows.length).toBe(4);
    expect(mockedText()).not.toContain("etc/nginx");
    expect(mockedText()).not.toContain(".env");
    expect(mockedText()).toContain(REDACTED_MARK);
    expect(mockedText()).toContain("敏感");
    for (const el of m.container.querySelectorAll("[title]")) {
      expect(el.getAttribute("title")).not.toContain(".env");
    }
    expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/");

    const serverRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("server.js"),
    );
    if (!serverRow) throw new Error("server.js row not found");
    expect(serverRow.className).not.toContain("cursor-pointer");
    expect(serverRow.textContent).toContain("文件");
    click(serverRow);
    await flush();
    expect(mocks.listDir).not.toHaveBeenCalledWith("s1", containerA.id, "/server.js");

    const etcRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("etc"),
    );
    if (!etcRow) throw new Error("etc row not found");
    click(etcRow);
    await waitFor(() => expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/etc"));
    await waitFor(() => expect(mockedText()).toContain(REDACTED_MARK));
    expect(mockedText()).not.toContain("passwd");
    const passwdRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes(REDACTED_MARK),
    );
    if (!passwdRow) throw new Error("masked passwd row not found");
    expect(passwdRow.getAttribute("title")).toBe("已遮蔽");
    expect(passwdRow.textContent).toContain("文件");
    click(passwdRow);
    await flush();
    expect(mocks.listDir).not.toHaveBeenCalledWith("s1", containerA.id, "/etc/passwd");
  });

  it("files tab masks sensitive directory names in rows and breadcrumbs but navigates by raw value", async () => {
    mocks.listDir.mockImplementation((_s: string, _c: string, path: string) => {
      if (path === "/") return Promise.resolve(["secrets/", "app/", "id_rsa", ".env.production", "cert.pem"]);
      if (path === "/secrets") return Promise.resolve(["token.txt"]);
      return Promise.resolve([]);
    });
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "文件");
    await waitFor(() => expect(mockedText()).toContain("app"));

    expect(mockedText()).not.toContain(".env.production");
    expect(mockedText()).not.toContain("id_rsa");
    expect(mockedText()).not.toContain("cert.pem");
    expect(mockedText()).not.toContain("secrets");

    const secretsRow = [...m.container.querySelectorAll("tbody tr")].find(
      (r) => r.textContent?.includes("敏感") && r.textContent?.includes("目录"),
    );
    if (!secretsRow) throw new Error("masked secrets row not found");
    expect(secretsRow.getAttribute("title")).toBe("已遮蔽");
    click(secretsRow);
    await waitFor(() => expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/secrets"));
    expect(mockedText()).toContain(REDACTED_MARK);
    expect(mockedText()).not.toContain("secrets");
    expect(mockedText()).not.toContain("token.txt");
    for (const el of m.container.querySelectorAll("[title]")) {
      expect(el.getAttribute("title")).not.toContain("secrets");
    }
  });

  it("files tab never leaks a sensitive parent path via descendant titles, breadcrumbs or error echo", async () => {
    mocks.listDir.mockImplementation((_s: string, _c: string, path: string) => {
      if (path === "/") return Promise.resolve(["secrets/"]);
      if (path === "/secrets") return Promise.resolve(["app/"]);
      if (path === "/secrets/app") return Promise.resolve(["server.js"]);
      return Promise.reject(new Error(`ls: ${path}: Not a directory`));
    });
    const m = (mounted = mountPanel());
    await openInsight(m, 0);
    clickButton(m.container, "文件");
    await waitFor(() => expect(mockedText()).toContain("敏感"));

    const secretsRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("敏感"),
    );
    if (!secretsRow) throw new Error("secrets row not found");
    click(secretsRow);
    await waitFor(() => expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/secrets"));

    await waitFor(() => expect(mockedText()).toContain("app"));
    const appRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("app"),
    );
    if (!appRow) throw new Error("app row not found");
    expect(appRow.getAttribute("title")).toBe(`/${REDACTED_MARK}/app`);
    expect(appRow.getAttribute("title")).not.toContain("secrets");

    click(appRow);
    await waitFor(() => expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/secrets/app"));
    await waitFor(() => expect(mockedText()).toContain("server.js"));
    const titles = [...m.container.querySelectorAll("[title]")].map(
      (el) => el.getAttribute("title") ?? "",
    );
    expect(titles.some((t) => t === `上级：/${REDACTED_MARK}`)).toBe(true);
    expect(titles.some((t) => t === `/${REDACTED_MARK}/app`)).toBe(true);
    for (const t of titles) expect(t).not.toContain("secrets");

    const serverRow = [...m.container.querySelectorAll("tbody tr")].find((r) =>
      r.textContent?.includes("server.js"),
    );
    if (!serverRow) throw new Error("server.js row not found");
    expect(serverRow.className).not.toContain("cursor-pointer");
    expect(serverRow.getAttribute("title")).toBe(
      `/${REDACTED_MARK}/app/server.js（文件，仅列出目录，不读取内容）`,
    );
    click(serverRow);
    await flush();
    expect(mocks.listDir).not.toHaveBeenCalledWith("s1", containerA.id, "/secrets/app/server.js");

    expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/secrets");
    expect(mocks.listDir).toHaveBeenCalledWith("s1", containerA.id, "/secrets/app");
    expect(mockedText()).not.toContain("secrets");
  });

  it("files tab shows inaccessible path errors with retry and a way back", async () => {
    mocks.listDir.mockImplementation((_s: string, _c: string, path: string) => {
      if (path === "/") return Promise.resolve(["etc/"]);
      if (path === "/etc") return Promise.resolve(["shadow/"]);
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

  it("toolbar summary says 加载中 only while pending and 加载失败 after a failure", async () => {
    const pending = deferred<ContainerSummary[]>();
    mocks.ps.mockReturnValue(pending.promise);
    mounted = mountPanel();

    await waitFor(() => expect(mockedText()).toContain("容器状态加载中"));
    pending.resolve([containerA]);
    await waitFor(() => expect(mockedText()).toContain("1 运行中 / 共 1"));
    expect(mockedText()).not.toContain("容器状态加载中");
  });

  it("failed containers query turns the toolbar summary into 加载失败 with an actionable daemon hint", async () => {
    mocks.ps.mockRejectedValueOnce(
      new Error("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"),
    );
    mounted = mountPanel();

    await waitFor(() => expect(mockedText()).toContain("容器状态加载失败"));
    expect(mockedText()).not.toContain("容器状态加载中");
    expect(mockedText()).toContain("连不上这台主机的 Docker 守护进程");
  });

  it("missing docker binary maps to an actionable not-installed hint", async () => {
    mocks.ps.mockRejectedValueOnce(new Error("bash: docker: command not found"));
    mounted = mountPanel();

    await waitFor(() => expect(mockedText()).toContain("容器列表加载失败"));
    expect(mockedText()).toContain("这台主机没有安装 Docker");
  });

  it("log follow surfaces an interrupted state with retry when re-attach fails after reconnect", async () => {
    mocks.logsAttach.mockResolvedValueOnce("tab-1").mockRejectedValueOnce(new Error("ssh: session down"));
    const m = (mounted = mountPanel());
    await waitFor(() => expect(m.container.querySelector('button[title="查看日志"]')).not.toBeNull());

    click(m.container.querySelector('button[title="查看日志"]')!);
    await flush();
    await waitFor(() => expect(mockedText()).toContain("跟随中 · 关闭此标签或返回即停止"));

    mocks.reopenCbs[mocks.reopenCbs.length - 1]();
    await flush();
    await waitFor(() => expect(mockedText()).toContain("跟随中断"));
    expect(mockedText()).toContain("ssh: session down");
    expect(mockedText()).not.toContain("跟随中 · 关闭此标签或返回即停止");

    mocks.logsAttach.mockResolvedValueOnce("tab-2");
    clickButton(m.container, "重新跟随");
    await flush();
    await waitFor(() => expect(mockedText()).toContain("跟随中 · 关闭此标签或返回即停止"));
    expect(mockedText()).not.toContain("跟随中断");
  });

  it("log follow ends instead of claiming 跟随中 once the container is no longer running", async () => {
    mocks.logsAttach.mockResolvedValue("tab-1");
    const m = (mounted = mountPanel());
    await waitFor(() => expect(m.container.querySelectorAll('button[title="查看日志"]').length).toBe(2));

    click(m.container.querySelectorAll('button[title="查看日志"]')[1]);
    await flush();
    await waitFor(() => expect(mockedText()).toContain("跟随中 · 关闭此标签或返回即停止"));

    mocks.reopenCbs[mocks.reopenCbs.length - 1]();
    await flush();
    await waitFor(() => expect(mockedText()).toContain("跟随结束"));
    expect(mockedText()).toContain("容器已停止，不再产生新日志");
    expect(mockedText()).not.toContain("跟随中断");
    expect(mocks.logsAttach).toHaveBeenCalledTimes(1);
  });

  function mockedText(): string {
    return mounted?.container.textContent ?? "";
  }
});

describe("destructive delete confirmations (R42, real DialogHost)", () => {
  const imageA: ImageSummary = {
    id: "sha256:aaa111",
    repository: "nginx",
    tag: "1.25",
    size: "50MB",
    createdSince: "2 days ago",
  };
  const imageUntagged: ImageSummary = {
    id: "sha256:bbb222",
    repository: "<none>",
    tag: "<none>",
    size: "10MB",
    createdSince: "1 day ago",
  };
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast, appDialog: null });
    mocks.ps.mockResolvedValue([containerA, containerB]);
    mocks.images.mockResolvedValue([imageA]);
    mocks.overview.mockResolvedValue({
      containers: [containerA, containerB],
      hostStats: { containersRunning: 1, containersTotal: 2, images: 1 },
    });
    mocks.action.mockResolvedValue(undefined);
    mocks.imageRemove.mockResolvedValue(undefined);
    mocks.ask.mockImplementation((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) =>
      mocks.realAsk!(message, options),
    );
    registerDialogHandlers({
      ask: (message, options) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "ask",
            message,
            title: options?.title,
            level: dialogLevelForKind(options?.kind),
            resolve,
          });
        }),
      confirm: (message) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({ kind: "confirm", message, level: "warning", resolve });
        }),
      message: (message) =>
        new Promise<void>((resolve) => {
          useUi
            .getState()
            .openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
        }),
      choose: vi.fn(),
    });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  function mountPanelWithDialogHost(): MountedView {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement("div", null, createElement(DockerPanel, { sessionId: "s1", visible: true }), createElement(DialogHost)),
      ),
    );
  }

  async function openModal(): Promise<HTMLElement> {
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull(),
    );
    return mounted!.container.querySelector<HTMLElement>(".nx-modal")!;
  }

  async function closeModal(modal: HTMLElement, button: "取消" | "确定"): Promise<void> {
    clickButton(modal, button);
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal")).toBeNull(),
    );
  }

  function segmentItem(container: ParentNode, label: string): HTMLButtonElement {
    const button = [...container.querySelectorAll(".nx-segment-item")].find((b) =>
      b.textContent?.includes(label),
    );
    if (!button) throw new Error(`Segment item not found: ${label}`);
    return button as HTMLButtonElement;
  }

  it("container remove renders an alertdialog, cancel aborts", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="删除"]')).not.toBeNull());

    click(m.container.querySelector('button[title="删除"]')!);
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("删除容器 web");
    expect(modal.textContent).toContain("挂载卷和主机目录不会被删除");
    await closeModal(modal, "取消");
    expect(mocks.action).not.toHaveBeenCalled();

    click(m.container.querySelector('button[title="删除"]')!);
    const modal2 = await openModal();
    expect(modal2.getAttribute("role")).toBe("alertdialog");
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("s1", containerA.id, "remove"));
  });

  it("image remove renders an alertdialog, cancel aborts", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="删除"]')).not.toBeNull());
    click(segmentItem(m.container, "镜像"));
    await waitFor(() => expect(m.container.querySelector('button[title="删除镜像"]')).not.toBeNull());

    click(m.container.querySelector('button[title="删除镜像"]')!);
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("删除镜像 nginx:1.25");
    expect(modal.textContent).toContain("仍可用来创建容器");
    await closeModal(modal, "取消");
    expect(mocks.imageRemove).not.toHaveBeenCalled();

    click(m.container.querySelector('button[title="删除镜像"]')!);
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.imageRemove).toHaveBeenCalledWith("s1", "nginx:1.25", false));
  });

  it("untagged image remove explains the image itself is deleted", async () => {
    mocks.images.mockResolvedValue([imageA, imageUntagged]);
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="删除"]')).not.toBeNull());
    click(segmentItem(m.container, "镜像"));
    await waitFor(() => expect(m.container.querySelectorAll('button[title="删除镜像"]').length).toBe(2));

    click(m.container.querySelectorAll('button[title="删除镜像"]')[1]);
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("删除镜像 sha256:bbb222");
    expect(modal.textContent).toContain("镜像本体");
    expect(modal.textContent).toContain("不能再用它创建容器");
    await closeModal(modal, "取消");
    expect(mocks.imageRemove).not.toHaveBeenCalled();

    click(m.container.querySelectorAll('button[title="删除镜像"]')[1]);
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.imageRemove).toHaveBeenCalledWith("s1", "sha256:bbb222", false));
  });

  it("image remove explains the last-tag deletes the image itself", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="删除"]')).not.toBeNull());
    click(segmentItem(m.container, "镜像"));
    await waitFor(() => expect(m.container.querySelector('button[title="删除镜像"]')).not.toBeNull());

    click(m.container.querySelector('button[title="删除镜像"]')!);
    const modal = await openModal();
    expect(modal.textContent).toContain("删除镜像 nginx:1.25");
    expect(modal.textContent).toContain("最后一个标签");
    expect(modal.textContent).toContain("镜像本体会一并删除");
    expect(modal.textContent).toContain("仍可用来创建容器");
    await closeModal(modal, "取消");
    expect(mocks.imageRemove).not.toHaveBeenCalled();
  });

  it("bulk image remove for tagged-only selection explains the last-tag consequence", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="删除"]')).not.toBeNull());
    click(segmentItem(m.container, "镜像"));
    await waitFor(() => expect(m.container.querySelector('button[title="删除镜像"]')).not.toBeNull());

    const pickA = m.container.querySelector<HTMLInputElement>('input[aria-label="选择 nginx:1.25"]');
    if (!pickA) throw new Error("pick checkbox not found");
    click(pickA);
    await waitFor(() => expect(m.container.textContent).toContain("已选 1"));

    clickButton(m.container, "删除选中 (1)");
    const modal = await openModal();
    expect(modal.textContent).toContain("删除选中的 1 个镜像");
    expect(modal.textContent).toContain("最后一个标签");
    expect(modal.textContent).toContain("仍可用来创建容器");
    await closeModal(modal, "取消");
    expect(mocks.imageRemove).not.toHaveBeenCalled();
  });

  it("stop asks for confirmation with consequences, cancel aborts", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="停止"]')).not.toBeNull());

    click(m.container.querySelector('button[title="停止"]')!);
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("停止容器 web");
    expect(modal.textContent).toContain("服务会中断");
    expect(modal.textContent).toContain("可以重新启动");
    await closeModal(modal, "取消");
    expect(mocks.action).not.toHaveBeenCalled();

    click(m.container.querySelector('button[title="停止"]')!);
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("s1", containerA.id, "stop"));
  });

  it("restart asks for confirmation with consequences", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="重启"]')).not.toBeNull());

    click(m.container.querySelector('button[title="重启"]')!);
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("重启容器 web");
    expect(modal.textContent).toContain("先停止再启动");
    await closeModal(modal, "确定");
    await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("s1", containerA.id, "restart"));
  });

  it("bulk remove renders an alertdialog with the count, cancel aborts", async () => {
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelectorAll('button[title="删除"]').length).toBe(2));

    const pickA = m.container.querySelector<HTMLInputElement>('input[aria-label="选择 web"]');
    const pickB = m.container.querySelector<HTMLInputElement>('input[aria-label="选择 worker"]');
    if (!pickA || !pickB) throw new Error("pick checkboxes not found");
    click(pickA);
    click(pickB);
    await waitFor(() => expect(m.container.textContent).toContain("已选 2"));

    clickButton(m.container, "删除选中 (2)");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("删除选中的 2 个容器");
    expect(modal.textContent).toContain("挂载卷和主机目录不会被删除");
    await closeModal(modal, "取消");
    expect(mocks.action).not.toHaveBeenCalled();

    clickButton(m.container, "删除选中 (2)");
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.action).toHaveBeenCalledTimes(2));
    expect(mocks.action).toHaveBeenCalledWith("s1", containerA.id, "remove");
    expect(mocks.action).toHaveBeenCalledWith("s1", containerB.id, "remove");
  });

  it("bulk image remove with mixed tagged and untagged covers both consequences", async () => {
    mocks.images.mockResolvedValue([imageA, imageUntagged]);
    const m = (mounted = mountPanelWithDialogHost());
    await waitFor(() => expect(m.container.querySelector('button[title="删除"]')).not.toBeNull());
    click(segmentItem(m.container, "镜像"));
    await waitFor(() => expect(m.container.querySelectorAll('button[title="删除镜像"]').length).toBe(2));

    const pickA = m.container.querySelector<HTMLInputElement>('input[aria-label="选择 nginx:1.25"]');
    const pickU = m.container.querySelector<HTMLInputElement>('input[aria-label="选择 <none>:<none>"]');
    if (!pickA || !pickU) throw new Error("pick checkboxes not found");
    click(pickA);
    click(pickU);
    await waitFor(() => expect(m.container.textContent).toContain("已选 2"));

    clickButton(m.container, "删除选中 (2)");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("删除选中的 2 个镜像");
    expect(modal.textContent).toContain("镜像本体");
    expect(modal.textContent).toContain("最后一个标签");
    expect(modal.textContent).toContain("镜像本体会一并删除");
    expect(modal.textContent).toContain("不能再用它创建容器");
    expect(modal.textContent).toContain("仍可用来创建容器");
    await closeModal(modal, "取消");
    expect(mocks.imageRemove).not.toHaveBeenCalled();
  });
});

describe("dockerRedact helpers", () => {
  it("isSensitiveName redacts credential-like names but spares innocent lookalikes", () => {
    expect(isSensitiveName("POSTGRES_PASSWORD")).toBe(true);
    expect(isSensitiveName("GITHUB_TOKEN")).toBe(true);
    expect(isSensitiveName("DbPassword")).toBe(true);
    expect(isSensitiveName("SSH_AUTH_SOCK")).toBe(true);
    expect(isSensitiveName("PASSPHRASE")).toBe(true);
    expect(isSensitiveName("apikey")).toBe(true);
    expect(isSensitiveName("apitoken")).toBe(true);
    expect(isSensitiveName("x-api-token")).toBe(true);
    expect(isSensitiveName("KEYBOARD_LAYOUT")).toBe(false);
    expect(isSensitiveName("author")).toBe(false);
    expect(isSensitiveName("monkey")).toBe(false);
    expect(isSensitiveName("compass")).toBe(false);
    expect(isSensitiveName("bypass")).toBe(false);
  });

  it("redactUrlCredentials masks URL-embedded passwords and leaves plain URLs alone", () => {
    expect(redactUrlCredentials("postgres://admin:s3cret@db:5432/app")).toBe(
      `postgres://admin:${REDACTED_MARK}@db:5432/app`,
    );
    expect(redactUrlCredentials("redis://:pass@host:6379")).toBe(`redis://:${REDACTED_MARK}@host:6379`);
    expect(redactUrlCredentials("redis://:@host:6379")).toBe("redis://:@host:6379");
    expect(redactUrlCredentials("https://example.com/x")).toBe("https://example.com/x");
    expect(redactUrlCredentials("postgres://admin@db:5432/app")).toBe("postgres://admin@db:5432/app");
    expect(redactUrlCredentials("no url here")).toBe("no url here");
  });

  it("buildInspectViewModel redacts URL-embedded credentials in env, labels and the JSON view", () => {
    const raw = {
      Name: "/db",
      Config: {
        Image: "postgres:16",
        Env: [
          "DATABASE_URL=postgres://admin:s3cret@db:5432/app",
          "REPLICA_URL=mysql://root:pw@replica/db",
          "REDIS_URL=redis://:topsecret@cache:6379",
        ],
        Labels: {
          "metrics.url": "http://user:hunter2@metrics:9090/metrics",
          "cache.dsn": "redis://:labelpass@cache:6379",
        },
      },
      Mounts: [],
    };
    const vm = buildInspectViewModel(raw);
    expect(vm.env[0].value).toBe(`postgres://admin:${REDACTED_MARK}@db:5432/app`);
    expect(vm.env[1].value).toBe(`mysql://root:${REDACTED_MARK}@replica/db`);
    expect(vm.env[2].value).toBe(`redis://:${REDACTED_MARK}@cache:6379`);
    expect(vm.labels[0].value).toBe(`http://user:${REDACTED_MARK}@metrics:9090/metrics`);
    expect(vm.labels[1].value).toBe(`redis://:${REDACTED_MARK}@cache:6379`);
    expect(vm.json).not.toContain("s3cret");
    expect(vm.json).not.toContain("hunter2");
    expect(vm.json).not.toContain("pw@replica");
    expect(vm.json).not.toContain("topsecret");
    expect(vm.json).not.toContain("labelpass");
    expect(vm.json).toContain("DATABASE_URL");
  });

  it("redactInspectTree masks URL-embedded credentials in non-sensitive env entries", () => {
    const out = redactInspectTree({
      Config: { Env: ["DATABASE_URL=postgres://admin:s3cret@db/", "PATH=/usr/bin"] },
    }) as { Config: { Env: string[] } };
    expect(out.Config.Env[0]).toBe(`DATABASE_URL=postgres://admin:${REDACTED_MARK}@db/`);
    expect(out.Config.Env[1]).toBe("PATH=/usr/bin");
  });

  it("masks sensitive env entries, label values and mount name/source segments but keeps structure", () => {
    const input = {
      Config: {
        Env: ["PATH=/usr/bin", "API_SECRET=s3cr3t", "NO_VALUE"],
        Labels: { "com.example.ok": "fine", "auth.token": "t0k" },
      },
      Nested: { DbPassword: "p@ss" },
      HostConfig: { Binds: ["db-passwords:/secrets:rw", "/home/alice/app:/app"] },
      Mounts: [
        {
          Type: "volume",
          Name: "db-passwords",
          Source: "/var/lib/docker/volumes/db-passwords/_data",
          Destination: "/secrets",
          RW: false,
        },
        { Type: "bind", Name: "", Source: "/home/alice/app", RW: true },
      ],
    };
    const out = redactInspectTree(input) as typeof input;
    expect(out.Config.Env[0]).toBe("PATH=/usr/bin");
    expect(out.Config.Env[1]).toBe(`API_SECRET=${REDACTED_MARK}`);
    expect(out.Config.Env[2]).toBe("NO_VALUE");
    expect(out.Config.Labels["com.example.ok"]).toBe("fine");
    expect(out.Config.Labels["auth.token"]).toBe(REDACTED_MARK);
    expect(out.Nested.DbPassword).toBe(REDACTED_MARK);
    expect(out.HostConfig.Binds[0]).toBe(`${REDACTED_MARK}:/secrets:rw`);
    expect(out.HostConfig.Binds[1]).toBe("/home/alice/app:/app");
    expect(out.Mounts[0].Name).toBe(REDACTED_MARK);
    expect(out.Mounts[0].Source).toBe(`/var/lib/docker/volumes/${REDACTED_MARK}/_data`);
    expect(out.Mounts[0].Destination).toBe("/secrets");
    expect(out.Mounts[1].Source).toBe("/home/alice/app");
  });

  it("redactBindEntry masks only the source of a bind spec", () => {
    expect(redactBindEntry("db-passwords:/secrets")).toBe(`${REDACTED_MARK}:/secrets`);
    expect(redactBindEntry("db-passwords:/secrets:ro")).toBe(`${REDACTED_MARK}:/secrets:ro`);
    expect(redactBindEntry("/home/alice/secret-keys:/keys:ro")).toBe(
      `/home/alice/${REDACTED_MARK}:/keys:ro`,
    );
    expect(redactBindEntry("/tmp/nx-acc-data:/data")).toBe("/tmp/nx-acc-data:/data");
  });

  it("redactMountSource masks only sensitive path segments", () => {
    expect(redactMountSource("/var/lib/docker/volumes/db-passwords/_data")).toBe(
      `/var/lib/docker/volumes/${REDACTED_MARK}/_data`,
    );
    expect(redactMountSource("/home/alice/app")).toBe("/home/alice/app");
    expect(redactMountSource("")).toBe("");
  });

  it("redactFileName masks the name, keeping only non-sensitive extensions", () => {
    expect(redactFileName(".env.production")).toBe(REDACTED_MARK);
    expect(redactFileName("id_rsa")).toBe(REDACTED_MARK);
    expect(redactFileName("cert.pem")).toBe(REDACTED_MARK);
    expect(redactFileName("passwd")).toBe(REDACTED_MARK);
    expect(redactFileName("app_secret.yaml")).toBe(`${REDACTED_MARK}.yaml`);
  });

  it("redactContainerPath masks every sensitive segment, not just the last", () => {
    expect(redactContainerPath("/secrets/app")).toBe(`/${REDACTED_MARK}/app`);
    expect(redactContainerPath("/secrets/app/server.js")).toBe(`/${REDACTED_MARK}/app/server.js`);
    expect(redactContainerPath("/home/alice/app")).toBe("/home/alice/app");
    expect(redactContainerPath("/")).toBe("/");
  });

  it("redactPathInText replaces the raw path in backend error echoes", () => {
    expect(redactPathInText("ls: /secrets/app: Permission denied", "/secrets")).toBe(
      `ls: /${REDACTED_MARK}/app: Permission denied`,
    );
    expect(redactPathInText("ls: /secrets: Not a directory", "/secrets")).toBe(
      `ls: /${REDACTED_MARK}: Not a directory`,
    );
    expect(redactPathInText("daemon down", "/secrets")).toBe("daemon down");
    expect(redactPathInText("ls: /: Permission denied", "/")).toBe("ls: /: Permission denied");
  });

  it("buildInspectViewModel redacts everything the DOM renders, with real mount shapes", () => {
    const vm = buildInspectViewModel(inspectA);
    expect(vm.name).toBe("web");
    expect(vm.image).toBe("nginx:1.25");
    expect(vm.env).toEqual([
      { key: "PATH", value: "/usr/bin" },
      { key: "POSTGRES_PASSWORD", value: REDACTED_MARK },
      { key: "TZ", value: "UTC" },
    ]);
    expect(vm.labels).toEqual([
      { key: "com.docker.compose.project", value: "shop" },
      { key: "x-api-token", value: REDACTED_MARK },
    ]);
    expect(vm.mounts[0]).toMatchObject({
      name: REDACTED_MARK,
      source: `/var/lib/docker/volumes/${REDACTED_MARK}/_data`,
      rw: false,
    });
    expect(vm.mounts[1]).toMatchObject({ source: "/home/alice/app", rw: true });
    expect(vm.json).not.toContain("hunter2secret");
    expect(vm.json).not.toContain("tok-123456");
    expect(vm.json).not.toContain("db-passwords");
    expect(vm.json).toContain("POSTGRES_PASSWORD");
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
