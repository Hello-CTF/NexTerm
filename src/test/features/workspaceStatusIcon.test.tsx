/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
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

import App, { aiToastPlacement } from "../../app/App";
import { useUi } from "../../app/store";

function session(status: SessionInfo["status"]): SessionInfo {
  return { id: "s1", assetId: null, name: "web-01", kind: "ssh", status, tabs: [], createdAt: 0 };
}

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function statusImg(): HTMLElement | null {
  return document.querySelector<HTMLElement>('.nx-ws-status[role="img"]');
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
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
        sessionId: "s1",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [{ id: "t1", kind: "settings", title: "设置", closable: true }],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("workspace status non-color distinctions", () => {
  const cases: { status: SessionInfo["status"]; label: string; tone: string }[] = [
    { status: "connected", label: "已连接", tone: "is-ok" },
    { status: "failed", label: "连接失败", tone: "is-bad" },
    { status: "connecting", label: "连接中", tone: "is-warn" },
    { status: "reconnecting", label: "重连中", tone: "is-warn" },
    { status: "disconnected", label: "已断开", tone: "is-warn" },
  ];

  it.each(cases)("renders $label as an icon with text alternative", async ({ status, label, tone }) => {
    mocks.sessionList.mockResolvedValue([session(status)]);
    useUi.setState({ sessions: [session(status)] });
    mounted = mountApp();
    await flush();

    const img = statusImg();
    expect(img).not.toBeNull();
    expect(img?.getAttribute("aria-label")).toBe(label);
    expect(img?.classList.contains(tone)).toBe(true);
    expect(img?.querySelector("svg")).not.toBeNull();
  });

  it("gives every distinct state a distinct glyph", async () => {
    const glyphs = new Map<string, string>();
    for (const { status } of cases) {
      mocks.sessionList.mockResolvedValue([session(status)]);
      useUi.setState({ sessions: [session(status)] });
      mounted = mountApp();
      await flush();
      const img = statusImg();
      const svg = img?.querySelector("svg");
      const key = `${svg?.innerHTML ?? ""}|${svg?.getAttribute("class") ?? ""}`;
      glyphs.set(status, key);
      mounted.unmount();
      mounted = undefined;
    }
    expect(glyphs.get("connecting")).toBe(glyphs.get("reconnecting"));
    expect(new Set(glyphs.values()).size).toBe(4);
  });

  it("marks in-progress states with a spinner, not only a color", async () => {
    for (const status of ["connecting", "reconnecting"] as const) {
      mocks.sessionList.mockResolvedValue([session(status)]);
      useUi.setState({ sessions: [session(status)] });
      mounted = mountApp();
      await flush();
      expect(statusImg()?.querySelector("svg")?.getAttribute("class")).toContain("animate-spin");
      mounted.unmount();
      mounted = undefined;
    }
    mocks.sessionList.mockResolvedValue([session("connected")]);
    useUi.setState({ sessions: [session("connected")] });
    mounted = mountApp();
    await flush();
    expect(statusImg()?.querySelector("svg")?.getAttribute("class") ?? "").not.toContain(
      "animate-spin",
    );
  });

  it("renders no status icon when the workspace has no session entry", async () => {
    mocks.sessionList.mockResolvedValue([]);
    useUi.setState({ sessions: [] });
    mounted = mountApp();
    await flush();
    expect(statusImg()).toBeNull();
  });
});

describe("toast avoidance wiring", () => {
  it("flags the app root when the AI dock is open and publishes its width", async () => {
    mocks.sessionList.mockResolvedValue([]);
    useUi.setState({ sessions: [], rightOpen: true, rightWidth: 352 });
    mounted = mountApp();
    await flush();

    const root = document.querySelector<HTMLElement>(".nx-app");
    expect(root?.getAttribute("data-nx-ai")).toBe("dock");
    expect(root?.style.getPropertyValue("--nx-right-w")).toBe("352px");
  });

  it("clears the flag when the AI dock is closed", async () => {
    mocks.sessionList.mockResolvedValue([]);
    useUi.setState({ sessions: [], rightOpen: false, rightWidth: 352 });
    mounted = mountApp();
    await flush();

    const root = document.querySelector<HTMLElement>(".nx-app");
    expect(root?.getAttribute("data-nx-ai")).toBeNull();
  });
});

describe("aiToastPlacement", () => {
  it("is null while the AI dock is closed", () => {
    expect(aiToastPlacement(1280, false, false, 352)).toBeNull();
  });

  it("uses overlay placement whenever sidebars overlay", () => {
    expect(aiToastPlacement(390, true, true, 352)).toBe("overlay");
    expect(aiToastPlacement(560, true, true, 352)).toBe("overlay");
  });

  it("docks beside the sidebar while at least 200px remain", () => {
    expect(aiToastPlacement(1280, false, true, 352)).toBe("dock");
    expect(aiToastPlacement(1100, false, true, 760)).toBe("dock");
    expect(aiToastPlacement(900, false, true, 668)).toBe("dock");
  });

  it("falls back to the left edge when the sidebar eats the viewport", () => {
    expect(aiToastPlacement(900, false, true, 760)).toBe("left");
    expect(aiToastPlacement(821, false, true, 760)).toBe("left");
  });
});
