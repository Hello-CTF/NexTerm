/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  connect: vi.fn(),
  reconnect: vi.fn(),
  probeHostKey: vi.fn(),
  knownHostAccept: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {
    list: mocks.assetList,
    search: mocks.assetSearch,
    knownHostAccept: mocks.knownHostAccept,
  },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: mocks.connect,
    reconnect: mocks.reconnect,
    probeHostKey: mocks.probeHostKey,
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  syncApi: {
    linkGet: vi.fn().mockResolvedValue({
      url: "",
      username: "",
      insecure: false,
      hasPassword: false,
      verifiedAt: 0,
      lastError: null,
    }),
    status: vi.fn().mockResolvedValue({ configured: false, loggedIn: false, lastError: "", warnings: [] }),
    kindOptInGet: vi.fn().mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false }),
    wake: vi.fn().mockResolvedValue(undefined),
  },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { connectAsset, useUi } from "../../app/store";

const PENDING_DETAIL = {
  host: "10.0.0.8",
  port: 22,
  keyType: "ssh-ed25519",
  fingerprint: "SHA256:newfp",
  changed: false,
};
const SESSION = {
  id: "s1",
  assetId: "a1",
  name: "web-01",
  kind: "ssh",
  status: "failed" as const,
  tabs: [],
  createdAt: 0,
};
const FRESH_SESSION = { ...SESSION, id: "s2", status: "connected" };
// After a successful reconnect the session id is unchanged; the backend reconnect first sets the state to reconnecting, and frontend polling reads the terminal state.
const RECONNECTED_SESSION = { ...SESSION, status: "connected" as const };

// The list reads the old state until reconnect is called and only then reads connected,
// so the App-mount list cannot flip the session to connected and take the alive fast path.
function listUntilReconnectThenConnected(): void {
  mocks.sessionList.mockImplementation(async () =>
    mocks.reconnect.mock.calls.length > 0 ? [RECONNECTED_SESSION] : [SESSION],
  );
}

function seedWorkspace(withSession: boolean): void {
  useUi.setState({
    sessions: withSession ? [SESSION] : [],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        ...(withSession ? { sessionId: "s1" } : {}),
        assetId: "a1",
        assetKind: "ssh",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [{ id: "t1", kind: "settings", title: "Settings", closable: true }],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
}

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function dialog(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[role="alertdialog"]');
}

function clickWorkspaceNewTerminal(): void {
  const button = document.querySelector<HTMLButtonElement>(
    'button[aria-label="新建终端标签"]',
  );
  if (!button) throw new Error("Workspace new terminal button not found");
  click(button);
}

function toastTexts(): string {
  return useUi
    .getState()
    .toasts.map((t) => t.text)
    .join("\n");
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([SESSION]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ appDialog: null, toasts: [] });
});

describe("Host key confirmation when creating a terminal (App openNewTerminal)", () => {
  it("shows the fingerprint dialog for a disconnected ssh session in pending state and does not reconnect after cancellation", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("首次连接 10.0.0.8:22");
    expect(text).toContain("SHA256:newfp");
    expect(mocks.reconnect).not.toHaveBeenCalled();

    clickButton(dialog() as HTMLElement, "取消");
    await waitFor(() => expect(dialog()).toBeNull());
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");
  });

  it("shows the changed-key warning with old and new fingerprints, then records the key and reconnects after acceptance", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "changed",
      known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:oldfp" }],
    });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("主机 10.0.0.8:22 的密钥已变更");
    expect(text).toContain("SHA256:oldfp");
    expect(text).toContain("SHA256:newfp");

    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
  });

  it("reconnects directly without a dialog when the fingerprint is unchanged", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "known",
    });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(dialog()).toBeNull();
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
  });

  it("sends the probed public key with the accept after confirmation", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      key: "ZmFrZS1rZXktdGVzdA==",
      state: "pending",
    });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
      "ZmFrZS1rZXktdGVzdA==",
    );
  });

  it("shows confirmation when probe itself reports host_key_pending for a jump host and does not reconnect after cancellation", async () => {
    seedWorkspace(true);
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
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("首次连接 jump.example:22");
    expect(text).toContain("SHA256:jumpfp");
    expect(mocks.reconnect).not.toHaveBeenCalled();

    clickButton(dialog() as HTMLElement, "取消");
    await waitFor(() => expect(dialog()).toBeNull());
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");
  });

  it("shows old and new fingerprints when probe reports changed, then records the key and reconnects after acceptance", async () => {
    seedWorkspace(true);
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
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("主机 jump.example:22 的密钥已变更");
    expect(text).toContain("SHA256:jumpold");
    expect(text).toContain("SHA256:jumpnew");

    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "jump.example",
      22,
      "ssh-ed25519",
      "SHA256:jumpnew",
    );
  });

  it("falls back to direct reconnect without a dialog when probing is unavailable", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockRejectedValue({ code: "not_found", message: "unknown command" });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(dialog()).toBeNull();
  });

  it("reconnects non-ssh sessions directly without probing the fingerprint", async () => {
    seedWorkspace(true);
    mocks.sessionList.mockImplementation(async () =>
      mocks.reconnect.mock.calls.length > 0
        ? [{ ...SESSION, kind: "winrm", status: "connected" as const }]
        : [{ ...SESSION, kind: "winrm" }],
    );
    mocks.reconnect.mockResolvedValue(true);
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.probeHostKey).not.toHaveBeenCalled();
    expect(dialog()).toBeNull();
  });

  it("handles host_key_pending from connect for an asset-only workspace, aborting on cancellation and retrying to open a terminal on acceptance", async () => {
    seedWorkspace(false);
    mocks.sessionList.mockResolvedValue([]);
    mocks.connect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: PENDING_DETAIL,
      })
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: PENDING_DETAIL,
      })
      .mockResolvedValueOnce(FRESH_SESSION);
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());
    expect(dialog()?.textContent).toContain("首次连接 10.0.0.8:22");
    expect(mocks.connect).toHaveBeenCalledTimes(1);

    clickButton(dialog() as HTMLElement, "取消");
    await waitFor(() => expect(dialog()).toBeNull());
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());
    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(3));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    await waitFor(() =>
      expect(useUi.getState().sessions.some((s) => s.id === "s2")).toBe(true),
    );
  });
});

describe("Host key confirmation dialog deduplication", () => {
  it("shares the existing connectAsset dialog when creating a terminal instead of opening another", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mocks.connect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: PENDING_DETAIL,
      })
      .mockResolvedValueOnce(FRESH_SESSION);
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    let connectPromise!: ReturnType<typeof connectAsset>;
    act(() => {
      connectPromise = connectAsset({ id: "a1", name: "web-01", kind: "ssh" });
    });
    await flush();
    expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(1);

    clickButton(dialog() as HTMLElement, "确定");
    await act(async () => {
      await connectPromise;
    });
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));

    expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(0);
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.probeHostKey).toHaveBeenCalledTimes(1);
  });

  it("does not connect through either path when the shared dialog is cancelled", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mocks.connect.mockRejectedValue({
      code: "host_key_pending",
      message: "unknown SSH host key for 10.0.0.8:22",
      detail: PENDING_DETAIL,
    });
    mounted = mountApp();
    await flush();

    clickWorkspaceNewTerminal();
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    let connectPromise!: ReturnType<typeof connectAsset>;
    act(() => {
      connectPromise = connectAsset({ id: "a1", name: "web-01", kind: "ssh" });
    });
    await flush();
    expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(1);

    clickButton(dialog() as HTMLElement, "取消");
    await act(async () => {
      await connectPromise;
    });
    await flush();

    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).not.toHaveBeenCalled();
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");
    expect(toastTexts()).toContain("已取消连接");
  });
});
