/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  flush,
  flushUntil,
  mount,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  dockerPs: vi.fn(),
  dockerImages: vi.fn(),
  dockerOverview: vi.fn(),
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  mountList: vi.fn(),
  auditQuery: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      ps: mocks.dockerPs,
      images: mocks.dockerImages,
      overview: mocks.dockerOverview,
      action: vi.fn(),
      imageRemove: vi.fn(),
      imagePull: vi.fn(),
      logsAttach: vi.fn(),
    },
    forwardApi: {
      env: mocks.forwardEnv,
      list: mocks.forwardList,
      create: vi.fn(),
      createSocks: vi.fn(),
      remove: vi.fn(),
    },
    mountApi: {
      list: mocks.mountList,
      create: vi.fn(),
      remove: vi.fn(),
    },
    assetApi: {
      auditQuery: mocks.auditQuery,
    },
    sessionApi: {},
    terminalApi: {},
    vaultApi: {},
  };
});

import { DockerPanel } from "../../features/docker/DockerPanel";
import { ForwardPanel } from "../../features/forward/ForwardPanel";
import { MountPanel } from "../../features/files/MountPanel";
import { AuditView } from "../../features/settings/AuditView";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.dockerPs.mockResolvedValue([]);
  mocks.dockerImages.mockResolvedValue([]);
  mocks.dockerOverview.mockResolvedValue({ hostStats: {} });
  mocks.forwardEnv.mockResolvedValue({ available: true, listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([]);
  mocks.mountList.mockResolvedValue([]);
  mocks.auditQuery.mockResolvedValue([]);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

describe("DockerPanel 查询状态", () => {
  it("shows the real error with retry instead of the empty state when docker ps fails", async () => {
    mocks.dockerPs.mockRejectedValue(new Error("daemon 未运行"));
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("容器列表加载失败 · daemon 未运行"));
    expect(text()).not.toContain("这台主机上还没有容器");
  });

  it("retry recovers into the true empty state", async () => {
    mocks.dockerPs.mockRejectedValueOnce(new Error("daemon 未运行"));
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("容器列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("这台主机上还没有容器"));
    expect(mocks.dockerPs).toHaveBeenCalledTimes(2);
  });

  it("shows a loading row while the first containers query is in flight", async () => {
    let resolvePs!: (v: unknown[]) => void;
    mocks.dockerPs.mockImplementation(
      () => new Promise<unknown[]>((resolve) => (resolvePs = resolve)),
    );
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("加载中…"));
    resolvePs([]);
    await flushUntil(() => text().includes("这台主机上还没有容器"));
  });

  it("images tab gets the same error/retry treatment", async () => {
    mocks.dockerImages.mockRejectedValue(new Error("镜像仓库不可达"));
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flush();
    const imagesTab = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("镜像"),
    );
    expect(imagesTab).toBeTruthy();
    click(imagesTab!);
    await flushUntil(() => text().includes("镜像仓库不可达"));
    expect(text()).not.toContain("本机没有镜像");
  });
});

describe("ForwardPanel 查询状态", () => {
  it("shows error plus retry instead of the empty state when the list query fails", async () => {
    mocks.forwardList.mockRejectedValue(new Error("转发服务离线"));
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("转发列表加载失败 · 转发服务离线"));
    expect(text()).not.toContain("还没有任何转发");
  });

  it("retry recovers into the true empty state", async () => {
    mocks.forwardList.mockRejectedValueOnce(new Error("转发服务离线"));
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("转发列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("还没有任何转发"));
  });

  it("associates the listen-port label with its input", async () => {
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s1" }));
    await flush();
    const label = [...mounted!.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("本地端口"),
    );
    expect(label).toBeTruthy();
    const input = mounted!.container.querySelector<HTMLInputElement>(
      `input[id="${label!.htmlFor}"]`,
    );
    expect(input).not.toBeNull();
    expect(input!.autocomplete).toBe("off");
  });

  it("两个实例同挂时 label 各自关联到本实例的输入框", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement(
          "div",
          null,
          createElement("div", { id: "fp-1" }, createElement(ForwardPanel, { sessionId: "s1" })),
          createElement("div", { id: "fp-2" }, createElement(ForwardPanel, { sessionId: "s2" })),
        ),
      ),
    );
    await flush();
    const htmlForOf = (panelId: string) => {
      const panel = mounted!.container.querySelector(`#${panelId}`)!;
      const label = [...panel.querySelectorAll("label")].find((l) =>
        l.textContent?.includes("本地端口"),
      );
      expect(label, `${panelId} 缺少本地端口 label`).toBeTruthy();
      const input = panel.querySelector<HTMLInputElement>(`input[id="${label!.htmlFor}"]`);
      expect(input, `${panelId} 的 label 必须解析到本实例内的输入框`).not.toBeNull();
      return label!.htmlFor;
    };
    expect(htmlForOf("fp-1")).not.toBe(htmlForOf("fp-2"));
  });
});

describe("MountPanel 查询状态", () => {
  it("shows error plus retry instead of the empty state when the list query fails", async () => {
    mocks.mountList.mockRejectedValue(new Error("挂载服务异常"));
    mounted = withClient(createElement(MountPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("挂载列表加载失败 · 挂载服务异常"));
    expect(text()).not.toContain("本机当前没有映射盘");
  });

  it("retry recovers into the true empty state", async () => {
    mocks.mountList.mockRejectedValueOnce(new Error("挂载服务异常"));
    mounted = withClient(createElement(MountPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("挂载列表加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("本机当前没有映射盘"));
  });
});

describe("AuditView 查询状态", () => {
  it("surfaces the load failure with retry instead of the empty hint", async () => {
    mocks.auditQuery.mockRejectedValue(new Error("审计库不可用"));
    mounted = withClient(createElement(AuditView));
    await flushUntil(() => text().includes("审计记录加载失败 · 审计库不可用"));
    expect(text()).not.toContain("暂无记录");
  });

  it("retry after failure reaches the true empty state", async () => {
    mocks.auditQuery.mockRejectedValueOnce(new Error("审计库不可用"));
    mounted = withClient(createElement(AuditView));
    await flushUntil(() => text().includes("审计记录加载失败"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("暂无记录"));
  });

  it("renders rows on success", async () => {
    mocks.auditQuery.mockResolvedValue([
      {
        id: 1,
        ts: 1700000000000,
        sessionId: "s1",
        assetId: "a1",
        source: "user",
        kind: "exec",
        payload: { cmd: "ls" },
        exitCode: 0,
        durationMs: 12,
      },
    ]);
    mounted = withClient(createElement(AuditView));
    await flushUntil(() => text().includes("exec"));
    expect(text()).toContain("共 1 条");
  });
});
