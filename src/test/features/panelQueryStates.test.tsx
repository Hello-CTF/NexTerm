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
    systemApi: { platform: vi.fn().mockResolvedValue("windows") },
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

describe("DockerPanel query states", () => {
  it("shows the real error with retry instead of the empty state when docker ps fails", async () => {
    mocks.dockerPs.mockRejectedValue(new Error("daemon unavailable"));
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("未能加载容器列表：daemon unavailable"));
    expect(text()).not.toContain("该主机上暂无容器");
    expect(text()).toContain("未能加载容器状态");
    expect(text()).not.toContain("正在加载容器状态");
  });

  it("shows a neutral state instead of an error when the host has no Docker", async () => {
    mocks.dockerPs.mockRejectedValue({ code: "docker_not_installed", message: "该主机没有安装 Docker" });
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("该主机没有安装 Docker，安装后请重试。"));
    expect(text()).not.toContain("加载失败");
    expect(text()).not.toContain("内部错误");
    expect(text()).toContain("未安装 Docker");
  });

  it("explains a host whose Docker daemon is not running", async () => {
    mocks.dockerPs.mockRejectedValue({ code: "docker_unavailable", message: "Docker 未运行" });
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("该主机未运行 Docker，启动后请重试。"));
    expect(text()).not.toContain("加载失败");
    expect(text()).toContain("未运行 Docker");
  });

  it("retry recovers into the true empty state", async () => {
    mocks.dockerPs.mockRejectedValueOnce(new Error("daemon unavailable"));
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("未能加载容器列表"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("该主机上暂无容器"));
    expect(mocks.dockerPs).toHaveBeenCalledTimes(2);
  });

  it("shows a loading row while the first containers query is in flight", async () => {
    let resolvePs!: (v: unknown[]) => void;
    mocks.dockerPs.mockImplementation(
      () => new Promise<unknown[]>((resolve) => (resolvePs = resolve)),
    );
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("正在加载…"));
    resolvePs([]);
    await flushUntil(() => text().includes("该主机上暂无容器"));
  });

  it("images tab gets the same error/retry treatment", async () => {
    mocks.dockerImages.mockRejectedValue(new Error("image registry unreachable"));
    mounted = withClient(createElement(DockerPanel, { sessionId: "s1" }));
    await flush();
    const imagesTab = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("镜像"),
    );
    expect(imagesTab).toBeTruthy();
    click(imagesTab!);
    await flushUntil(() => text().includes("image registry unreachable"));
    expect(text()).not.toContain("本机没有镜像");
  });
});

describe("ForwardPanel query states", () => {
  it("shows error plus retry instead of the empty state when the list query fails", async () => {
    mocks.forwardList.mockRejectedValue(new Error("forward service offline"));
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("未能加载转发列表：forward service offline"));
    expect(text()).not.toContain("暂无任何转发");
    expect(text()).not.toContain("正在加载转发列表");
  });

  it("retry recovers into the true empty state", async () => {
    mocks.forwardList.mockRejectedValueOnce(new Error("forward service offline"));
    mounted = withClient(createElement(ForwardPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("未能加载转发列表"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("暂无任何转发"));
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

  it("associates each label with its own instance's input when two instances are mounted", async () => {
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
      expect(label, `${panelId} is missing the local port label`).toBeTruthy();
      const input = panel.querySelector<HTMLInputElement>(`input[id="${label!.htmlFor}"]`);
      expect(input, `${panelId} label must resolve to an input in this instance`).not.toBeNull();
      return label!.htmlFor;
    };
    expect(htmlForOf("fp-1")).not.toBe(htmlForOf("fp-2"));
  });
});

describe("MountPanel query states", () => {
  it("shows error plus retry instead of the empty state when the list query fails", async () => {
    mocks.mountList.mockRejectedValue(new Error("mount service failure"));
    mounted = withClient(createElement(MountPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("未能加载挂载列表：mount service failure"));
    expect(text()).not.toContain("当前会话没有挂载点");
  });

  it("retry recovers into the true empty state", async () => {
    mocks.mountList.mockRejectedValueOnce(new Error("mount service failure"));
    mounted = withClient(createElement(MountPanel, { sessionId: "s1" }));
    await flushUntil(() => text().includes("未能加载挂载列表"));
    clickButton(mounted!.container, "重试");
    await flushUntil(() => text().includes("当前会话没有挂载点"));
    expect(text()).toContain("新建挂载");
  });
});

describe("AuditView query states", () => {
  it("surfaces the load failure with retry instead of the empty hint", async () => {
    mocks.auditQuery.mockRejectedValue(new Error("audit database unavailable"));
    mounted = withClient(createElement(AuditView));
    await flushUntil(() => text().includes("未能加载审计记录：audit database unavailable"));
    expect(text()).not.toContain("暂无记录");
  });

  it("retry after failure reaches the true empty state", async () => {
    mocks.auditQuery.mockRejectedValueOnce(new Error("audit database unavailable"));
    mounted = withClient(createElement(AuditView));
    await flushUntil(() => text().includes("未能加载审计记录"));
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
