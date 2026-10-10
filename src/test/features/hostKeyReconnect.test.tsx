/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  connect: vi.fn(),
  reconnect: vi.fn(),
  probeHostKey: vi.fn(),
  knownHostAccept: vi.fn(),
  attachDead: false,
}));

vi.mock("../../app/platform", () => ({
  isMac: () => false,
  modHint: () => "Ctrl",
  setMacPlatform: () => {},
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { knownHostAccept: mocks.knownHostAccept },
  dbApi: {},
  vaultApi: {},
  sessionApi: {
    connect: mocks.connect,
    reconnect: mocks.reconnect,
    disconnect: vi.fn(),
    probeHostKey: mocks.probeHostKey,
  },
  terminalApi: {
    listLive: vi.fn().mockResolvedValue([]),
    write: vi.fn().mockResolvedValue(undefined),
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("../../ipc/events", () => ({
  listenEvent: vi.fn().mockResolvedValue(() => {}),
  EVENTS: { terminalControl: "terminal-control" },
  EventVersionGate: class {
    accept() {
      return true;
    }
  },
}));

vi.mock("../../ipc/env", () => ({ clientId: () => "me", WEB: false }));

vi.mock("../../ui/dialogs", () => ({
  describeTarget: () => "",
  finishSave: vi.fn(),
  pickSavePath: vi.fn(),
  promptText: vi.fn(),
  ask: mocks.ask,
}));

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      useEffect(() => {
        if (mocks.attachDead) {
          (props.onAttachFailed as ((code: string) => void) | undefined)?.("not_found");
        } else {
          (props.onAttach as ((id: string) => void) | undefined)?.("kernel-1");
        }
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";

const PENDING_DETAIL = {
  host: "10.0.0.8",
  port: 22,
  keyType: "ssh-ed25519",
  fingerprint: "SHA256:newfp",
  changed: false,
};
const CHANGED_DETAIL = {
  ...PENDING_DETAIL,
  changed: true,
  known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:oldfp" }],
};

function seed(
  overrides: {
    status?: "connected" | "failed" | "disconnected";
    kind?: string;
    assetId?: string | null;
  } = {},
): void {
  const { status = "connected", kind = "ssh", assetId = "a1" } = overrides;
  useUi.setState({
    sessions: [
      {
        id: "s1",
        assetId,
        name: "web-01",
        kind,
        status,
        tabs: [],
        createdAt: 0,
      },
    ],
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        sessionId: "s1",
        assetId: "a1",
        assetKind: kind,
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [
              {
                id: "t1",
                kind: "terminal",
                title: "term",
                sessionId: "s1",
                tabId: "kernel-1",
                closable: true,
              },
            ],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
}

function toastTexts(): string {
  return useUi
    .getState()
    .toasts.map((t) => t.text)
    .join("\n");
}

async function clickTerminalMenuItem(label: string): Promise<void> {
  const body = document.querySelector<HTMLElement>(".nx-terminal-body .relative");
  if (!body) throw new Error("terminal body not found");
  act(() => {
    body.dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }),
    );
  });
  await flush();
  const item = [...document.querySelectorAll<HTMLElement>(".nx-menu .nx-menu-item")].find(
    (candidate) => candidate.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item ${label} not found`);
  act(() => item.dispatchEvent(new MouseEvent("click", { bubbles: true })));
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.attachDead = false;
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ toasts: [] });
});

describe("TerminalPane 失效终端重连（reconnectNow）主机指纹确认", () => {
  beforeEach(() => {
    mocks.attachDead = true;
    seed();
    mounted = mount(
      createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
    );
  });

  it("connect 报 host_key_pending 时弹确认，取消则不重试", async () => {
    mocks.connect.mockRejectedValueOnce({
      code: "host_key_pending",
      message: "unknown SSH host key for 10.0.0.8:22",
      detail: PENDING_DETAIL,
    });
    mocks.ask.mockResolvedValue(false);
    await flush();

    const button = [...document.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.includes("重新连接这台主机"),
    );
    if (!button) throw new Error("reconnect button not found");
    act(() => button.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));

    const [question, options] = mocks.ask.mock.calls[0] as [string, Record<string, unknown>];
    expect(question).toContain("首次连接 10.0.0.8:22");
    expect(question).toContain("SHA256:newfp");
    expect(options).toMatchObject({ title: "确认主机指纹", kind: "warning" });

    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
  });

  it("接受后记账并重试，终端按新会话重置", async () => {
    mocks.connect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "SSH host key for 10.0.0.8:22 changed",
        detail: CHANGED_DETAIL,
      })
      .mockResolvedValueOnce({
        id: "s2",
        assetId: "a1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
      });
    mocks.ask.mockResolvedValue(true);
    await flush();

    const button = [...document.querySelectorAll("button")].find(
      (candidate) => candidate.textContent?.includes("重新连接这台主机"),
    );
    if (!button) throw new Error("reconnect button not found");
    act(() => button.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    mocks.attachDead = false;
    await waitFor(() => expect(toastTexts()).toContain("已重新连接"));

    const [question] = mocks.ask.mock.calls[0] as [string];
    expect(question).toContain("主机 10.0.0.8:22 的密钥已变更");
    expect(question).toContain("SHA256:oldfp");
    expect(question).toContain("SHA256:newfp");
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    expect(useUi.getState().sessions.some((s) => s.id === "s2")).toBe(true);
    const tab = useUi
      .getState()
      .workspaces[0]?.panes[0]?.tabs.find((t) => t.id === "t1");
    expect(tab?.sessionId).toBe("s2");
    expect(tab?.dead).toBe(false);
  });
});

describe("TerminalPane 菜单重连（reconnectSession）主机指纹确认", () => {
  beforeEach(() => {
    seed({ status: "failed" });
    mounted = mount(
      createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
    );
  });

  it("pending 时取消则不重连", async () => {
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mocks.ask.mockResolvedValue(false);
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(mocks.ask.mock.calls[0]?.[0]).toContain("首次连接 10.0.0.8:22");
    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
  });

  it("changed 时接受后记账并重连", async () => {
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "changed",
      known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:oldfp" }],
    });
    mocks.ask.mockResolvedValue(true);
    mocks.reconnect.mockResolvedValue(true);
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));

    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("主机 10.0.0.8:22 的密钥已变更");
    expect(question).toContain("SHA256:oldfp");
    expect(question).toContain("SHA256:newfp");
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
  });

  it("指纹未变化时不弹窗直接重连", async () => {
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "known",
    });
    mocks.reconnect.mockResolvedValue(true);
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
  });

  it("probe 自身报 host_key_pending（跳板机）时取消则不重连", async () => {
    mocks.probeHostKey.mockRejectedValue({
      code: "host_key_pending",
      message: "unknown SSH host key for jump.example:22",
      detail: {
        host: "jump.example",
        port: 22,
        keyType: "ssh-ed25519",
        fingerprint: "SHA256:jumpfp",
        changed: false,
      },
    });
    mocks.ask.mockResolvedValue(false);
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(mocks.ask.mock.calls[0]?.[0]).toContain("首次连接 jump.example:22");
    expect(mocks.ask.mock.calls[0]?.[0]).toContain("SHA256:jumpfp");
    await waitFor(() => expect(toastTexts()).toContain("已取消重连"));
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
  });

  it("probe 报 changed 时接受后记账并重连", async () => {
    mocks.probeHostKey.mockRejectedValue({
      code: "host_key_pending",
      message: "SSH host key for jump.example:22 changed",
      detail: {
        host: "jump.example",
        port: 22,
        keyType: "ssh-ed25519",
        fingerprint: "SHA256:jumpnew",
        changed: true,
        known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:jumpold" }],
      },
    });
    mocks.ask.mockResolvedValue(true);
    mocks.reconnect.mockResolvedValue(true);
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));

    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("主机 jump.example:22 的密钥已变更");
    expect(question).toContain("SHA256:jumpold");
    expect(question).toContain("SHA256:jumpnew");
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "jump.example",
      22,
      "ssh-ed25519",
      "SHA256:jumpnew",
    );
  });

  it("探测不可用回退为直接重连", async () => {
    mocks.probeHostKey.mockRejectedValue({ code: "not_found", message: "unknown command" });
    mocks.reconnect.mockResolvedValue(true);
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("会话在点击前已被删除时说明真实原因而不是「请刷新后重试」", async () => {
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "known",
    });
    mocks.reconnect.mockRejectedValue({
      code: "not_found",
      message: "会话不存在或已关闭，请刷新后重试",
    });
    await flush();

    await clickTerminalMenuItem("重连会话");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    await waitFor(() => expect(toastTexts()).toContain("会话已在服务端删除"));
    expect(toastTexts()).not.toContain("请刷新后重试");
    expect(
      useUi.getState().toasts.some((t) => t.kind === "error" && t.text.includes("会话已在服务端删除")),
    ).toBe(true);
  });
});
