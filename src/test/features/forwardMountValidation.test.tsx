/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, click, deferred, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  forwardCreate: vi.fn(),
  forwardCreateSocks: vi.fn(),
  mountList: vi.fn(),
  mountCreate: vi.fn(),
  platform: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    forwardApi: {
      env: mocks.forwardEnv,
      list: mocks.forwardList,
      create: mocks.forwardCreate,
      createSocks: mocks.forwardCreateSocks,
      remove: vi.fn(),
    },
    mountApi: {
      list: mocks.mountList,
      create: mocks.mountCreate,
      remove: vi.fn(),
    },
    systemApi: { platform: mocks.platform },
    sessionApi: {},
  };
});

import { ForwardPanel } from "../../features/forward/ForwardPanel";
import { MountPanel } from "../../features/files/MountPanel";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  document.body.replaceChildren();
  mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([]);
  mocks.mountList.mockResolvedValue([]);
  mocks.platform.mockResolvedValue("windows");
  useUi.setState({ pushToast: mocks.toast, sessions: [], workspaces: [], activeWorkspaceId: null });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

function mountForward(): QueryClient {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(ForwardPanel, { sessionId: "s" })),
  );
  return client;
}

function mountMountPanel(sessionId: string | null = "s1"): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(MountPanel, { sessionId: sessionId ?? undefined }),
    ),
  );
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function blur(element: HTMLElement): void {
  act(() => {
    element.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  });
}

function fieldError(input: HTMLInputElement): HTMLElement | null {
  const id = input.getAttribute("aria-describedby");
  if (!id) return null;
  return document.getElementById(id);
}

function inputBy(selector: string): HTMLInputElement {
  const input = mounted!.container.querySelector<HTMLInputElement>(selector);
  if (!input) throw new Error(`input not found: ${selector}`);
  return input;
}

function setViewportWidth(width: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
}

describe("ForwardPanel 提交时内联校验", () => {
  it("监听端口非法时提交才报错：内联关联字段、焦点落在监听端口、不再弹 toast", async () => {
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    setInputValue(listenPort, "70000");
    expect(listenPort.getAttribute("aria-invalid")).toBeNull();
    clickButton(mounted!.container, "创建");
    expect(listenPort.getAttribute("aria-invalid")).toBe("true");
    const error = fieldError(listenPort);
    expect(error?.getAttribute("role")).toBe("alert");
    expect(error?.textContent).toBe("本地端口要填 1–65535 之间的整数");
    expect(listenPort.parentElement?.contains(error)).toBe(true);
    expect(document.activeElement).toBe(listenPort);
    expect(mocks.forwardCreate).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("监听端口失焦立即报错，无需等待", async () => {
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    setInputValue(listenPort, "abc");
    blur(listenPort);
    expect(listenPort.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(listenPort)?.textContent).toBe("本地端口要填 1–65535 之间的整数");
  });

  it("目标端口空闲约 500ms 后自动校验，无需失焦", async () => {
    mountForward();
    await flush();
    const targetPort = inputBy('input[aria-label="目标端口"]');
    setInputValue(targetPort, "22.5");
    await advance(499);
    expect(targetPort.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(targetPort)).toBeNull();
    await advance(1);
    expect(targetPort.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(targetPort)?.textContent).toBe("目标端口要填 1–65535 之间的整数");
  });

  it("修正后失焦立即清除错误", async () => {
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    setInputValue(listenPort, "abc");
    blur(listenPort);
    expect(fieldError(listenPort)).not.toBeNull();
    setInputValue(listenPort, "13306");
    blur(listenPort);
    expect(listenPort.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(listenPort)).toBeNull();
  });

  it("失焦报错后 env 才 resolve：既有 alert 文案随当前标签更新", async () => {
    const env = deferred<{ available: boolean; platform: string; listenHost: string }>();
    mocks.forwardEnv.mockReturnValue(env.promise);
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    setInputValue(listenPort, "70000");
    blur(listenPort);
    expect(fieldError(listenPort)?.textContent).toBe("本地端口要填 1–65535 之间的整数");
    env.resolve({ available: true, platform: "other", listenHost: "0.0.0.0" });
    await flush();
    expect(fieldError(listenPort)?.textContent).toBe("监听端口要填 1–65535 之间的整数");
  });

  it("输入后 500ms 内 env 切换：idle 回调使用切换后的标签", async () => {
    const env = deferred<{ available: boolean; platform: string; listenHost: string }>();
    mocks.forwardEnv.mockReturnValue(env.promise);
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    setInputValue(listenPort, "70000");
    env.resolve({ available: true, platform: "other", listenHost: "0.0.0.0" });
    await flush();
    expect(listenPort.getAttribute("aria-invalid")).toBeNull();
    await advance(500);
    expect(listenPort.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(listenPort)?.textContent).toBe("监听端口要填 1–65535 之间的整数");
  });

  it("多字段非法时焦点按字段顺序落在第一个非法字段", async () => {
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    const targetHost = inputBy('input[aria-label="目标主机"]');
    const targetPort = inputBy('input[aria-label="目标端口"]');
    setInputValue(listenPort, "70000");
    setInputValue(targetHost, " ");
    setInputValue(targetPort, "0");

    clickButton(mounted!.container, "创建");
    expect(listenPort.getAttribute("aria-invalid")).toBe("true");
    expect(targetHost.getAttribute("aria-invalid")).toBe("true");
    expect(targetPort.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(targetHost)?.textContent).toBe("目标主机不能为空");
    expect(document.activeElement).toBe(listenPort);

    setInputValue(listenPort, "13306");
    clickButton(mounted!.container, "创建");
    expect(document.activeElement).toBe(targetHost);

    setInputValue(targetHost, "10.0.0.8");
    clickButton(mounted!.container, "创建");
    expect(document.activeElement).toBe(targetPort);

    setInputValue(targetPort, "3306");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.forwardCreate).toHaveBeenCalledWith("s", 13306, "10.0.0.8", 3306);
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("SOCKS 模式只校验监听端口，目标字段不渲染", async () => {
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    clickButton(mounted!.container, "SOCKS5 代理");
    expect(mounted!.container.querySelector('input[aria-label="目标主机"]')).toBeNull();
    setInputValue(listenPort, "99999");
    clickButton(mounted!.container, "创建");
    expect(listenPort.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(listenPort)?.textContent).toBe("本地端口要填 1–65535 之间的整数");
    expect(document.activeElement).toBe(listenPort);
    expect(mocks.forwardCreateSocks).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("320/390 窄屏下错误文本折行不溢出", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      mountForward();
      await flush();
      const listenPort = inputBy('input[placeholder="13306"]');
      const targetPort = inputBy('input[aria-label="目标端口"]');
      setInputValue(listenPort, "70000");
      setInputValue(targetPort, "70000");
      blur(listenPort);
      blur(targetPort);
      for (const input of [listenPort, targetPort]) {
        const error = fieldError(input);
        expect(error?.className).toContain("break-words");
        expect(error?.className).not.toContain("whitespace-nowrap");
      }
      expect(listenPort.closest("div")?.className).toContain("shrink-0");
      expect(inputBy('input[aria-label="目标主机"]').closest("div")?.className).toContain("flex-1");
      mounted!.unmount();
      mounted = undefined;
    }
    setViewportWidth(1024);
  });
});

describe("MountPanel 提交时内联校验", () => {
  it("远端路径为空时提交：内联报错并聚焦远端路径，不再弹 toast", async () => {
    mountMountPanel();
    await flush();
    const remotePath = inputBy('input[aria-label="远端路径"]');
    clickButton(mounted!.container, "挂载");
    expect(remotePath.getAttribute("aria-invalid")).toBe("true");
    const error = fieldError(remotePath);
    expect(error?.getAttribute("role")).toBe("alert");
    expect(error?.textContent).toBe("远端路径不能为空");
    expect(remotePath.parentElement?.contains(error)).toBe(true);
    expect(document.activeElement).toBe(remotePath);
    expect(mocks.mountCreate).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("挂载点与远端路径都为空：两个字段都报错，焦点落在挂载点", async () => {
    mountMountPanel();
    await flush();
    const localPoint = inputBy('input[aria-label="本地挂载点"]');
    const remotePath = inputBy('input[aria-label="远端路径"]');
    setInputValue(localPoint, "");
    clickButton(mounted!.container, "挂载");
    expect(localPoint.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(localPoint)?.textContent).toBe("挂载点不能为空");
    expect(remotePath.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(remotePath)?.textContent).toBe("远端路径不能为空");
    expect(document.activeElement).toBe(localPoint);
    expect(mocks.mountCreate).not.toHaveBeenCalled();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("远端路径失焦立即报错，修正后失焦立即清除", async () => {
    mountMountPanel();
    await flush();
    const remotePath = inputBy('input[aria-label="远端路径"]');
    blur(remotePath);
    expect(remotePath.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(remotePath)?.textContent).toBe("远端路径不能为空");
    setInputValue(remotePath, "user@host:/data");
    blur(remotePath);
    expect(remotePath.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(remotePath)).toBeNull();
  });

  it("挂载点空闲约 500ms 后自动校验，无需失焦", async () => {
    mountMountPanel();
    await flush();
    const localPoint = inputBy('input[aria-label="本地挂载点"]');
    setInputValue(localPoint, " ");
    await advance(499);
    expect(localPoint.getAttribute("aria-invalid")).toBeNull();
    expect(fieldError(localPoint)).toBeNull();
    await advance(1);
    expect(localPoint.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(localPoint)?.textContent).toBe("挂载点不能为空");
  });

  it("合法输入提交成功且无内联错误", async () => {
    mocks.mountCreate.mockResolvedValue({ id: "m1" });
    mountMountPanel();
    await flush();
    setInputValue(inputBy('input[aria-label="远端路径"]'), "user@host:/data");
    clickButton(mounted!.container, "挂载");
    await flush();
    expect(mocks.mountCreate).toHaveBeenCalledWith({
      sessionId: "s1",
      remotePath: "user@host:/data",
      localPoint: "Z:",
      username: undefined,
      password: undefined,
    });
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("success", "已挂载 Z:");
  });

  it("320/390 窄屏下错误文本折行不溢出", async () => {
    for (const width of [320, 390]) {
      setViewportWidth(width);
      mountMountPanel();
      await flush();
      const localPoint = inputBy('input[aria-label="本地挂载点"]');
      const remotePath = inputBy('input[aria-label="远端路径"]');
      setInputValue(localPoint, "");
      blur(localPoint);
      blur(remotePath);
      for (const input of [localPoint, remotePath]) {
        const error = fieldError(input);
        expect(error?.className).toContain("break-words");
        expect(error?.className).not.toContain("whitespace-nowrap");
      }
      expect(localPoint.closest("div")?.className).toContain("shrink-0");
      expect(remotePath.closest("div")?.className).toContain("flex-1");
      expect(remotePath.closest("div")?.className).toContain("max-[560px]:min-w-[140px]");
      mounted!.unmount();
      mounted = undefined;
    }
    setViewportWidth(1024);
  });
});

describe("MountPanel 凭据折叠可访问性", () => {
  function credsToggle(): HTMLButtonElement {
    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("使用其他凭据"),
    );
    if (!toggle) throw new Error("凭据折叠按钮未找到");
    return toggle;
  }

  it("折叠按钮 aria-controls 指向凭据面板；已填凭据时折叠摘要显示已配置", async () => {
    mountMountPanel();
    await flush();
    const toggle = credsToggle();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    const panelId = toggle.getAttribute("aria-controls");
    expect(panelId).toBeTruthy();
    expect(toggle.textContent).not.toContain("已配置");

    click(toggle);
    await flush();
    const userInput = inputBy('input[aria-label="挂载用户名（可选）"]');
    expect(userInput.closest("div")?.id).toBe(panelId);

    setInputValue(userInput, "alice");
    click(toggle);
    await flush();
    expect(mounted!.container.querySelector('input[aria-label="挂载用户名（可选）"]')).toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toContain("已配置");
  });

  it("凭据保持为空时折叠摘要不显示已配置", async () => {
    mountMountPanel();
    await flush();
    const toggle = credsToggle();
    click(toggle);
    await flush();
    click(toggle);
    await flush();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).not.toContain("已配置");
  });
});

function actionButton(text: string): HTMLButtonElement {
  const button = [...mounted!.container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!button) throw new Error(`按钮未找到: ${text}`);
  return button;
}

function credentialsToggle(): HTMLButtonElement {
  const button = [...mounted!.container.querySelectorAll("button")].find((candidate) =>
    candidate.textContent?.includes("使用其他凭据"),
  );
  if (!button) throw new Error("凭据折叠按钮未找到");
  return button;
}

function selectWorkspaceTab(activeTabId: string): void {
  useUi.setState({
    activeWorkspaceId: "ws1",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws",
        sessionId: "s1",
        closable: true,
        activePaneId: "p1",
        splitRatio: 0.5,
        panes: [
          {
            id: "p1",
            activeTabId,
            tabs: [
              { id: "mount", kind: "mount", title: "挂载", sessionId: "s1", closable: true },
              { id: "forward", kind: "forward", title: "转发", sessionId: "s", closable: true },
              { id: "terminal", kind: "terminal", title: "终端", sessionId: "s1", closable: true },
            ],
          },
        ],
      },
    ],
  });
}

describe("MountPanel 平台默认值与连接约束", () => {
  it("Windows 默认 Z:，Linux 留空并使用 /mnt/point 示例", async () => {
    mountMountPanel();
    await flush();
    expect(inputBy('input[aria-label="本地挂载点"]').value).toBe("Z:");
    expect(inputBy('input[aria-label="本地挂载点"]').placeholder).toBe("Z:");

    mounted!.unmount();
    mounted = undefined;
    mocks.platform.mockResolvedValue("linux");
    mountMountPanel();
    await flush();
    const localPoint = inputBy('input[aria-label="本地挂载点"]');
    expect(localPoint.value).toBe("");
    expect(localPoint.placeholder).toBe("/mnt/point");
    expect(inputBy('input[aria-label="远端路径"]').placeholder).toBe("user@host:/data");
    const headers = [...mounted!.container.querySelectorAll("th")].map((th) => th.textContent?.trim());
    expect(headers).toContain("本地挂载点");
    expect(headers).not.toContain("挂载时间");
  });

  it("无 sessionId 或本地会话时禁用挂载并提示先连接 SSH 资产", async () => {
    mountMountPanel(null);
    await flush();
    expect(actionButton("挂载").disabled).toBe(true);
    expect(mounted!.container.textContent).toContain("请先连接 SSH 资产");

    mounted!.unmount();
    mounted = undefined;
    useUi.setState({
      sessions: [
        {
          id: "s1",
          assetId: null,
          name: "当前设备",
          kind: "local",
          status: "connected",
          tabs: [],
          createdAt: 1,
        },
      ],
    });
    mountMountPanel();
    await flush();
    expect(actionButton("挂载").disabled).toBe(true);
    expect(mounted!.container.textContent).toContain("请先连接 SSH 资产");
  });

  it("挂载成功清空本次凭据并收起，下一次请求不再携带", async () => {
    mocks.mountCreate.mockResolvedValue({ id: "m1" });
    mountMountPanel();
    await flush();
    const toggle = credentialsToggle();
    click(toggle);
    await flush();
    setInputValue(inputBy('input[aria-label="挂载用户名（可选）"]'), "alice");
    setInputValue(inputBy('input[aria-label="挂载密码（可选）"]'), "secret");
    setInputValue(inputBy('input[aria-label="远端路径"]'), "user@host:/one");
    clickButton(mounted!.container, "挂载");
    await flush();

    expect(mocks.mountCreate).toHaveBeenNthCalledWith(1, {
      sessionId: "s1",
      remotePath: "user@host:/one",
      localPoint: "Z:",
      username: "alice",
      password: "secret",
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).not.toContain("已配置");

    setInputValue(inputBy('input[aria-label="远端路径"]'), "user@host:/two");
    clickButton(mounted!.container, "挂载");
    await flush();
    expect(mocks.mountCreate).toHaveBeenNthCalledWith(2, {
      sessionId: "s1",
      remotePath: "user@host:/two",
      localPoint: "Z:",
      username: undefined,
      password: undefined,
    });
    click(toggle);
    await flush();
    expect(inputBy('input[aria-label="挂载用户名（可选）"]').value).toBe("");
    expect(inputBy('input[aria-label="挂载密码（可选）"]').value).toBe("");
  });

  it("隐藏时停止轮询，重新可见立即补取", async () => {
    selectWorkspaceTab("mount");
    mountMountPanel();
    await flush();
    expect(mocks.mountList).toHaveBeenCalledTimes(1);
    await advance(15000);
    expect(mocks.mountList).toHaveBeenCalledTimes(2);

    act(() => selectWorkspaceTab("terminal"));
    await advance(30000);
    expect(mocks.mountList).toHaveBeenCalledTimes(2);
    act(() => selectWorkspaceTab("mount"));
    await flush();
    expect(mocks.mountList).toHaveBeenCalledTimes(3);
  });
});

describe("ForwardPanel 环境状态、轮询与浏览器文案", () => {
  it("环境查询 pending 时禁用创建且不默认监听 127.0.0.1", async () => {
    const env = deferred<{ available: boolean; platform: string; listenHost: string }>();
    mocks.forwardEnv.mockReturnValue(env.promise);
    mountForward();
    await flush();
    expect(actionButton("创建").disabled).toBe(true);
    expect(mounted!.container.textContent).toContain("转发环境检查中");
    expect(mounted!.container.textContent).not.toContain("127.0.0.1:13306");

    env.resolve({ available: true, platform: "other", listenHost: "0.0.0.0" });
    await flush();
    expect(actionButton("创建").disabled).toBe(false);
    expect(mounted!.container.textContent).toContain("0.0.0.0:13306");
    expect(mocks.forwardCreate).not.toHaveBeenCalled();
  });

  it("环境查询失败时显示错误与重试，恢复前禁止创建", async () => {
    mocks.forwardEnv
      .mockRejectedValueOnce(new Error("环境探测失败"))
      .mockResolvedValueOnce({ available: true, platform: "other", listenHost: "0.0.0.0" });
    mountForward();
    await flush();
    expect(actionButton("创建").disabled).toBe(true);
    expect(mounted!.container.textContent).toContain("转发环境检查失败 · 环境探测失败");

    clickButton(mounted!.container, "重试");
    await flush();
    expect(mocks.forwardEnv).toHaveBeenCalledTimes(2);
    expect(actionButton("创建").disabled).toBe(false);
    expect(mounted!.container.textContent).toContain("0.0.0.0:13306");
  });

  it("后续刷新失败保留旧列表并显示非阻塞提示", async () => {
    mocks.forwardList
      .mockResolvedValueOnce([
        {
          id: "f1",
          sessionId: "s",
          listenHost: "127.0.0.1",
          listenPort: 13306,
          targetHost: "10.0.0.8",
          targetPort: 3306,
          kind: "local",
          createdAt: 1,
        },
      ])
      .mockRejectedValueOnce(new Error("暂时离线"));
    const client = mountForward();
    await flush();
    expect(mounted!.container.textContent).toContain("10.0.0.8");

    clickButton(mounted!.container, "刷新");
    await flush();
    expect(mocks.forwardList).toHaveBeenCalledTimes(2);
    expect(client.getQueryState(["forwards"])?.status).toBe("error");
    expect(mounted!.container.textContent).toContain("10.0.0.8");
    expect(mounted!.container.textContent).toContain("转发列表刷新失败 · 暂时离线");
    expect(mounted!.container.textContent).toContain("已保留上次成功结果");
    expect(actionButton("创建").disabled).toBe(false);
  });

  it("隐藏时停止轮询，重新可见立即补取", async () => {
    selectWorkspaceTab("forward");
    mountForward();
    await flush();
    expect(mocks.forwardList).toHaveBeenCalledTimes(1);
    await advance(5000);
    expect(mocks.forwardList).toHaveBeenCalledTimes(2);

    act(() => selectWorkspaceTab("terminal"));
    await advance(10000);
    expect(mocks.forwardList).toHaveBeenCalledTimes(2);
    act(() => selectWorkspaceTab("forward"));
    await flush();
    expect(mocks.forwardList).toHaveBeenCalledTimes(3);
  });

  it("Web 远程转发目标明确为 NexTerm 所在机器，静态转发提示一致", async () => {
    mocks.forwardCreate.mockResolvedValue({ id: "f1", kind: "local" });
    mountForward();
    await flush();
    clickButton(mounted!.container, "远程转发");
    expect(mounted!.container.textContent).toContain("NexTerm 所在机器");
    expect(mounted!.container.textContent).not.toContain("你本机");
    expect(mounted!.container.textContent).not.toContain("本地服务");
    expect(inputBy('input[aria-label="目标主机"]').placeholder).toContain("NexTerm 所在机器");

    clickButton(mounted!.container, "静态转发");
    clickButton(mounted!.container, "创建");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      "转发已就绪 → 127.0.0.1:13306（仅NexTerm 所在机器可访问）",
    );
  });
});
