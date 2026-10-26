/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  listAssets: vi.fn(),
  listLive: vi.fn(),
  switchEncoding: vi.fn(),
  write: vi.fn(),
  copy: vi.fn(),
  attachShell: null as "powershell" | "cmd" | "posix" | "unknown" | "missing" | null,
  attachRetainable: null as boolean | null,
  onAttach: null as ((id: string) => void) | null,
  attachInfo: null as
    | ((info: {
        tabId: string;
        controller: string | null;
        subscribers: number;
        viewers: number;
        exited: boolean;
        encoding?: string;
      }) => void)
    | null,
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
  assetApi: { list: harness.listAssets },
  dbApi: {},
  vaultApi: {},
  sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn() },
  terminalApi: {
    listLive: harness.listLive,
    write: harness.write,
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    switchEncoding: harness.switchEncoding,
    claim: vi.fn().mockResolvedValue(null),
    exportLog: vi.fn().mockResolvedValue(0),
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
  ask: vi.fn(),
  askChoice: vi.fn(),
}));

vi.mock("../../ui/clipboard", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/clipboard")>();
  return { ...actual, copyText: harness.copy };
});

vi.mock("../../features/terminal/CommandBlockPanel", () => ({ CommandBlockPanel: () => null }));
vi.mock("../../features/terminal/TerminalKeysBar", () => ({ TerminalKeysBar: () => null }));

vi.mock("../../features/terminal/XtermView", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    XtermView: (props: Record<string, unknown>) => {
      const onAttachInfoRef = useRef(props.onAttachInfo);
      const onAttachRef = useRef(props.onAttach);
      onAttachInfoRef.current = props.onAttachInfo;
      onAttachRef.current = props.onAttach;
      harness.attachInfo = props.onAttachInfo as typeof harness.attachInfo;
      harness.onAttach = props.onAttach as typeof harness.onAttach;
      useEffect(() => {
        if (harness.attachShell) {
          (onAttachInfoRef.current as ((info: unknown) => void) | undefined)?.({
            tabId: "kernel-1",
            controller: "me",
            subscribers: 1,
            viewers: 1,
            exited: false,
            shell: harness.attachShell === "missing" ? undefined : harness.attachShell,
            retainable: harness.attachRetainable ?? undefined,
          });
        }
        (onAttachRef.current as ((id: string) => void) | undefined)?.("kernel-1");
      }, []);
      return <div className="h-full w-full min-h-0" />;
    },
  };
});

import { TerminalPane } from "../../features/terminal/TerminalPane";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function badgeText(): string | null {
  const badge = [...document.querySelectorAll(".nx-badge-amber")].find((el) =>
    ["gbk", "big5", "gb18030", "latin1"].includes(el.textContent?.trim() ?? ""),
  );
  return badge?.textContent?.trim() ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  harness.attachInfo = null;
  harness.onAttach = null;
  harness.attachShell = null;
  harness.attachRetainable = null;
  harness.listLive.mockResolvedValue([]);
  harness.write.mockResolvedValue(undefined);
  harness.copy.mockResolvedValue(undefined);
  harness.switchEncoding.mockResolvedValue(undefined);
  harness.listAssets.mockResolvedValue([
    { id: "a1", kind: "ssh", name: "web-01", options: { encoding: "gbk" } },
  ]);
  useUi.setState({
    sessions: [
      {
        id: "s1",
        assetId: "a1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
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
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

function mountPane(): void {
  mounted = mount(
    createElement(TerminalPane, { sessionId: "s1", title: "term", storeTabId: "t1" }),
  );
}

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("terminal encoding badge", () => {
  it("shows the asset-configured encoding for a fresh terminal", async () => {
    mountPane();
    await flush();
    await waitFor(() => expect(badgeText()).toBe("gbk"));
  });

  it("shows no badge for utf-8 assets", async () => {
    harness.listAssets.mockResolvedValue([
      { id: "a1", kind: "ssh", name: "web-01", options: {} },
    ]);
    mountPane();
    await flush();
    expect(badgeText()).toBeNull();
  });

  it("follows the actual tab encoding reported on attach", async () => {
    harness.listAssets.mockResolvedValue([
      { id: "a1", kind: "ssh", name: "web-01", options: {} },
    ]);
    mountPane();
    await flush();
    expect(badgeText()).toBeNull();
    act(() => {
      harness.attachInfo?.({
        tabId: "kernel-1",
        controller: "me",
        subscribers: 1,
        viewers: 1,
        exited: false,
        encoding: "big5",
      });
    });
    await flush();
    expect(badgeText()).toBe("big5");
  });

  it("stores the attach background capability on the terminal tab", async () => {
    harness.attachShell = "posix";
    harness.attachRetainable = true;
    mountPane();
    await flush();

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.retainable).toBe(true);
  });
});

describe("pending directory navigation", () => {
  it("consumes a PowerShell directory exactly once across repeated attach callbacks", async () => {
    harness.attachShell = "powershell";
    useUi.getState().updateTab("t1", { pendingDirectory: "D:/Project [x]/child's dir" });
    mountPane();

    await waitFor(() => expect(harness.write).toHaveBeenCalledTimes(1));
    const [, data] = harness.write.mock.calls[0];
    expect(new TextDecoder().decode(data)).toBe(
      "Set-Location -LiteralPath 'D:/Project [x]/child''s dir'\n",
    );
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.pendingDirectory).toBeUndefined();

    act(() => harness.onAttach?.("kernel-1"));
    await flush();
    expect(harness.write).toHaveBeenCalledTimes(1);
  });

  it("makes a pending directory write failure visible", async () => {
    harness.attachShell = "posix";
    harness.write.mockRejectedValueOnce(new Error("write denied"));
    useUi.getState().updateTab("t1", { pendingDirectory: "/tmp/project" });
    mountPane();

    await waitFor(() => {
      const text = useUi.getState().toasts.map((toast) => toast.text).join("\n");
      expect(text).toContain("切换目录失败");
      expect(text).toContain("write denied");
    });
    expect(harness.write).toHaveBeenCalledTimes(1);
  });

  it.each(["cmd", "unknown"] as const)(
    "does not inject a pending directory into a %s terminal",
    async (shell) => {
      harness.attachShell = shell;
      useUi.getState().updateTab("t1", { pendingDirectory: "D:/Project [x]" });
      mountPane();
      await flush();

      expect(harness.write).not.toHaveBeenCalled();
      expect(harness.copy).toHaveBeenCalledWith("D:/Project [x]");
      expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.pendingDirectory).toBeUndefined();
      await waitFor(() => {
        const text = useUi.getState().toasts.map((toast) => toast.text).join("\n");
        expect(text).toContain("路径已复制");
      });
    },
  );

  it("surfaces a pending directory copy failure", async () => {
    harness.attachShell = "cmd";
    harness.copy.mockRejectedValueOnce(new Error("clipboard denied"));
    useUi.getState().updateTab("t1", { pendingDirectory: "D:/Project [x]" });
    mountPane();

    await waitFor(() => {
      const text = useUi.getState().toasts.map((toast) => toast.text).join("\n");
      expect(text).toContain("复制路径失败");
      expect(text).toContain("clipboard denied");
    });
    expect(harness.copy).toHaveBeenCalledWith("D:/Project [x]");
    expect(harness.write).not.toHaveBeenCalled();
  });

  it("treats missing shell metadata as unknown and does not inject", async () => {
    harness.attachShell = "missing";
    useUi.getState().updateTab("t1", { pendingDirectory: "D:/Project [x]" });
    mountPane();
    await flush();

    expect(harness.write).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.shell).toBe("unknown");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.pendingDirectory).toBeUndefined();
  });
});
