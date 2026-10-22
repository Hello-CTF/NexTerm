/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, click, deferred, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  forwardCreate: vi.fn(),
  forwardCreateSocks: vi.fn(),
  forwardRetry: vi.fn(),
  mountList: vi.fn(),
  mountCreate: vi.fn(),
  platform: vi.fn(),
  assetGet: vi.fn(),
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
      retry: mocks.forwardRetry,
      remove: vi.fn(),
    },
    mountApi: {
      list: mocks.mountList,
      create: mocks.mountCreate,
      remove: vi.fn(),
    },
    assetApi: { get: mocks.assetGet },
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

describe("ForwardPanel inline validation on submit", () => {
  it("invalid listen port errors only on submit: inline field linked, focus lands on the listen port, no toast", async () => {
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

  it("listen port errors immediately on blur, no waiting", async () => {
    mountForward();
    await flush();
    const listenPort = inputBy('input[placeholder="13306"]');
    setInputValue(listenPort, "abc");
    blur(listenPort);
    expect(listenPort.getAttribute("aria-invalid")).toBe("true");
    expect(fieldError(listenPort)?.textContent).toBe("本地端口要填 1–65535 之间的整数");
  });

  it("target port validates automatically after ~500ms idle, no blur needed", async () => {
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

  it("blur after fixing clears the error immediately", async () => {
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

  it("env resolves after the blur error: the existing alert text updates with the current label", async () => {
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

  it("env switches within 500ms of input: the idle callback uses the switched label", async () => {
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

  it("with multiple invalid fields focus lands on the first invalid field in order", async () => {
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

  it("SOCKS mode validates only the listen port; target fields are not rendered", async () => {
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

  it("error text wraps without overflow at 320/390 narrow widths", async () => {
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

describe("MountPanel inline validation on submit", () => {
  it("empty remote path on submit: inline error and focus on the remote path, no toast", async () => {
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

  it("empty mount point and remote path: both fields error, focus lands on the mount point", async () => {
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

  it("remote path errors immediately on blur; blur after fixing clears immediately", async () => {
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

  it("mount point validates automatically after ~500ms idle, no blur needed", async () => {
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

  it("valid input submits successfully with no inline errors", async () => {
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

  it("error text wraps without overflow at 320/390 narrow widths", async () => {
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

describe("MountPanel credential collapse accessibility", () => {
  function credsToggle(): HTMLButtonElement {
    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("使用其他凭据"),
    );
    if (!toggle) throw new Error("credential collapse button not found");
    return toggle;
  }

  it("collapse button aria-controls points at the credential panel; the summary shows configured when credentials are filled", async () => {
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

  it("summary does not show configured while credentials stay empty", async () => {
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
  if (!button) throw new Error(`button not found: ${text}`);
  return button;
}

function credentialsToggle(): HTMLButtonElement {
  const button = [...mounted!.container.querySelectorAll("button")].find((candidate) =>
    candidate.textContent?.includes("使用其他凭据"),
  );
  if (!button) throw new Error("credential collapse button not found");
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
              { id: "mount", kind: "mount", title: "Mount", sessionId: "s1", closable: true },
              { id: "forward", kind: "forward", title: "Forward", sessionId: "s", closable: true },
              { id: "terminal", kind: "terminal", title: "Terminal", sessionId: "s1", closable: true },
            ],
          },
        ],
      },
    ],
  });
}

describe("MountPanel platform defaults and connection constraints", () => {
  it("Windows defaults to Z:; Linux stays empty with a /mnt/point example", async () => {
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

  it("mount is disabled without sessionId or with a local session, prompting to connect an SSH asset first", async () => {
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
          name: "This Device",
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

  it("a successful mount clears this round of credentials and collapses; the next request carries none", async () => {
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

  it("polling stops when hidden and refetches immediately when visible again", async () => {
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

describe("MountPanel defaults the target to the current asset", () => {
  function useSshSession(): void {
    useUi.setState({
      sessions: [
        { id: "s1", assetId: "a1", name: "prod-host", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
  }

  function mockAsset(host: string, username: string | null): void {
    mocks.assetGet.mockResolvedValue({
      id: "a1",
      kind: "ssh",
      name: "prod-host",
      host,
      username,
    });
  }

  it("Linux prefills user@host: and names the target asset; Windows prefills \\\\host\\", async () => {
    useSshSession();
    mockAsset("10.0.0.8", "root");
    mocks.platform.mockResolvedValue("linux");
    mountMountPanel();
    await flushUntil(() => inputBy('input[aria-label="远端路径"]').value === "root@10.0.0.8:");
    expect(mounted!.container.textContent).toContain("默认目标为当前资产「prod-host」");

    mounted!.unmount();
    mounted = undefined;
    mocks.platform.mockResolvedValue("windows");
    mountMountPanel();
    await flushUntil(() => inputBy('input[aria-label="远端路径"]').value === "\\\\10.0.0.8\\");
  });

  it("IPv6 hosts are prefilled with brackets; no user@ without a username", async () => {
    useSshSession();
    mockAsset("2001:db8::1", null);
    mocks.platform.mockResolvedValue("linux");
    mountMountPanel();
    await flushUntil(() => inputBy('input[aria-label="远端路径"]').value === "[2001:db8::1]:");
  });

  it("asset resolution completing after user edits does not overwrite the input", async () => {
    useSshSession();
    mockAsset("10.0.0.8", "root");
    mocks.platform.mockResolvedValue("linux");
    mountMountPanel();
    await flushUntil(() => inputBy('input[aria-label="远端路径"]').value === "root@10.0.0.8:");
    const remotePath = inputBy('input[aria-label="远端路径"]');
    setInputValue(remotePath, "other:/x");
    await advance(1000);
    expect(remotePath.value).toBe("other:/x");
  });

  it("the current asset is prefilled again after a successful mount", async () => {
    useSshSession();
    mockAsset("10.0.0.8", "root");
    mocks.platform.mockResolvedValue("linux");
    mocks.mountCreate.mockResolvedValue({ id: "m1" });
    mountMountPanel();
    await flushUntil(() => inputBy('input[aria-label="远端路径"]').value === "root@10.0.0.8:");
    const remotePath = inputBy('input[aria-label="远端路径"]');
    setInputValue(inputBy('input[aria-label="本地挂载点"]'), "/mnt/data");
    setInputValue(remotePath, "root@10.0.0.8:/data");
    clickButton(mounted!.container, "挂载");
    await flush();
    expect(mocks.mountCreate).toHaveBeenCalledWith({
      sessionId: "s1",
      remotePath: "root@10.0.0.8:/data",
      localPoint: "/mnt/data",
      username: undefined,
      password: undefined,
    });
    expect(remotePath.value).toBe("root@10.0.0.8:");
  });
});

describe("ForwardPanel env status, polling, and browser wording", () => {
  it("create is disabled while the env query is pending and 127.0.0.1 is not the default listen address", async () => {
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

  it("falls back to the environment listenHost for runtime entries that omit it", async () => {
    mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "0.0.0.0" });
    mocks.forwardList.mockResolvedValue([
      {
        id: "legacy-forward",
        sessionId: "s",
        listenPort: 13306,
        targetHost: "10.0.0.8",
        targetPort: 3306,
        kind: "local",
        createdAt: 1,
      },
    ]);
    mountForward();
    await flush();

    const row = mounted!.container.querySelector("tbody tr");
    expect(row?.textContent).toContain("0.0.0.0:13306");
    expect(row?.textContent).not.toContain("127.0.0.1:13306");
  });

  it("env query failure shows the error and a retry; create stays forbidden until recovery", async () => {
    mocks.forwardEnv
      .mockRejectedValueOnce(new Error("env probe failed"))
      .mockResolvedValueOnce({ available: true, platform: "other", listenHost: "0.0.0.0" });
    mountForward();
    await flush();
    expect(actionButton("创建").disabled).toBe(true);
    expect(mounted!.container.textContent).toContain("转发环境检查失败 · env probe failed");

    clickButton(mounted!.container, "重试");
    await flush();
    expect(mocks.forwardEnv).toHaveBeenCalledTimes(2);
    expect(actionButton("创建").disabled).toBe(false);
    expect(mounted!.container.textContent).toContain("0.0.0.0:13306");
  });

  it("later refresh failures keep the old list and show a non-blocking notice", async () => {
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
      .mockRejectedValueOnce(new Error("temporarily offline"));
    const client = mountForward();
    await flush();
    expect(mounted!.container.textContent).toContain("10.0.0.8");

    clickButton(mounted!.container, "刷新");
    await flush();
    expect(mocks.forwardList).toHaveBeenCalledTimes(2);
    expect(client.getQueryState(["forwards"])?.status).toBe("error");
    expect(mounted!.container.textContent).toContain("10.0.0.8");
    expect(mounted!.container.textContent).toContain("转发列表刷新失败 · temporarily offline");
    expect(mounted!.container.textContent).toContain("已保留上次成功结果");
    expect(actionButton("创建").disabled).toBe(false);
  });

  it("polling stops when hidden and refetches immediately when visible again", async () => {
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

  it("web remote forwarding names the NexTerm host as the target; static forwarding wording stays consistent", async () => {
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

describe("ForwardPanel and MountPanel session scope", () => {
  function forwardRow(id: string, sessionId: string, targetHost: string) {
    return {
      id,
      sessionId,
      listenHost: "127.0.0.1",
      listenPort: 13306,
      targetHost,
      targetPort: 3306,
      kind: "local",
      createdAt: 1,
    };
  }

  function mountForwardSessionless(): void {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(ForwardPanel, {})),
    );
  }

  it("forward list shows only the current session by default and all sessions after switching", async () => {
    mocks.forwardList.mockResolvedValue([
      forwardRow("f1", "s", "10.0.0.8"),
      forwardRow("f2", "other", "10.0.0.9"),
    ]);
    mountForward();
    await flush();
    expect(mounted!.container.textContent).toContain("10.0.0.8");
    expect(mounted!.container.textContent).not.toContain("10.0.0.9");

    clickButton(mounted!.container, "仅当前会话");
    await flush();
    expect(mounted!.container.textContent).toContain("10.0.0.9");
    expect(
      [...mounted!.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "全部会话",
      ),
    ).toBe(true);
  });

  it("keeps a failed forward visible and retries it only through a connected session for the same asset", async () => {
    useUi.setState({ sessions: [{ id: "s", assetId: "asset-1", name: "owner", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }] });
    mocks.forwardList.mockResolvedValue([{
      ...forwardRow("f1", "old-session", "10.0.0.8"),
      hostId: "asset-1",
      status: "failed",
      error: "远端监听已断开",
    }]);
    mocks.forwardRetry.mockResolvedValue({ id: "f1" });
    mountForward();
    await flush();

    expect(mounted!.container.textContent).toContain("远端监听已断开");
    clickButton(mounted!.container, "重试");
    await flushUntil(() => mocks.toast.mock.calls.some(([kind, text]) => kind === "success" && text === "转发已恢复"));
    expect(mocks.forwardRetry.mock.calls.some(([id, connectedSession]) => id === "f1" && connectedSession === "s")).toBe(true);
    expect(mocks.toast).toHaveBeenCalledWith("success", "转发已恢复");
  });

  it("a sessionless forward tab shows all and renders no scope toggle button", async () => {
    mocks.forwardList.mockResolvedValue([
      forwardRow("f1", "s", "10.0.0.8"),
      forwardRow("f2", "other", "10.0.0.9"),
    ]);
    mountForwardSessionless();
    await flush();
    expect(mounted!.container.textContent).toContain("10.0.0.8");
    expect(mounted!.container.textContent).toContain("10.0.0.9");
    for (const label of ["仅当前会话", "全部会话"]) {
      expect(
        [...mounted!.container.querySelectorAll("button")].some(
          (b) => b.textContent?.trim() === label,
        ),
      ).toBe(false);
    }
  });

  it("mount list requests the current session and excludes unmanaged and other-session entries", async () => {
    mocks.mountList.mockResolvedValue([
      { id: "m1", localPoint: "Z:", remote: "owner@host:/data", sessionId: "s1", managed: true, createdAt: 1 },
      { id: "m2", localPoint: "Y:", remote: "system@host:/data", sessionId: null, managed: false, createdAt: null },
      { id: "m3", localPoint: "X:", remote: "other@host:/data", sessionId: "s2", managed: true, createdAt: 1 },
    ]);
    mountMountPanel("s1");
    await flush();
    expect(mocks.mountList).toHaveBeenCalledWith({ sessionId: "s1", forceRefresh: true });
    expect(mounted!.container.textContent).toContain("owner@host:/data");
    expect(mounted!.container.textContent).not.toContain("system@host:/data");
    expect(mounted!.container.textContent).not.toContain("other@host:/data");
    expect(
      [...mounted!.container.querySelectorAll("button")].filter(
        (button) => button.textContent?.trim() === "断开",
      ),
    ).toHaveLength(1);
  });

  it("sessionless mount panel shows a connection prompt without requesting a global mount list", async () => {
    mountMountPanel(null);
    await flush();
    expect(mocks.mountList).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).toContain("请先连接 SSH 资产后查看挂载");
  });
});
