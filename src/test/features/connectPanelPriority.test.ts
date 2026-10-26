/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sessionConnect: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: { connect: mocks.sessionConnect },
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import type { SessionInfo } from "../../ipc/commands";
import { connectAsset, useUi } from "../../app/store";
import { serializeLayout } from "../../app/layout";
import { resetSessionPanelForTest, showSessionPanel, useSessionPanel } from "../../features/session-panel/sessionPanelStore";

const SESSION: SessionInfo = {
  id: "s1",
  assetId: "a1",
  name: "test-host",
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 0,
};

const ASSET = { id: "a1", name: "test-host", kind: "ssh" };

function setViewportWidth(width: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  resetSessionPanelForTest();
  mocks.sessionConnect.mockResolvedValue(SESSION);
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    connectFocusRevision: 0,
  });
});

describe("Terminal priority after connecting on narrow screens", () => {
  it("preserves explicitly closed sidebars and panel modes without writing hidden preferences", async () => {
    setViewportWidth(500);
    useUi.setState({ leftOpen: false, leftMode: "assets", rightOpen: false });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(s.leftOpen).toBe(false);
    expect(s.leftMode).toBe("assets");
    expect(s.rightOpen).toBe(false);
    expect(s.connectFocusRevision).toBe(1);

    const ws = s.workspaces.find((w) => w.sessionId === SESSION.id);
    expect(s.activeWorkspaceId).toBe(ws?.id);
    expect(ws?.panes[0]?.tabs).toEqual([]);
  });

  it("keeps the original mode of an explicitly open sidebar instead of changing it to the file tree", async () => {
    setViewportWidth(500);
    useUi.setState({ leftOpen: true, leftMode: "credentials" });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(s.leftOpen).toBe(true);
    expect(s.leftMode).toBe("credentials");
  });

  it("does not change persisted panel preferences or widths when connecting on narrow screens", async () => {
    setViewportWidth(500);
    useUi.getState().setLeftWidth(300);
    useUi.getState().setRightWidth(420);
    useUi.setState({ leftOpen: false, leftMode: "assets", rightOpen: true });
    const before = localStorage.getItem("nexterm.layout.v1");

    await connectAsset(ASSET);

    const layout = serializeLayout(useUi.getState());
    expect(layout?.leftOpen).toBe(false);
    expect(layout?.leftMode).toBe("assets");
    expect(layout?.rightOpen).toBe(true);
    expect(layout?.leftWidth).toBe(300);
    expect(layout?.rightWidth).toBe(420);
    expect(localStorage.getItem("nexterm.layout.v1")).toBe(before);
  });
});

describe("Existing layout behavior on wide screens", () => {
  it("leaves the session panel closed after connecting when the user closed it by hand", async () => {
    setViewportWidth(1280);
    showSessionPanel(false);

    await connectAsset(ASSET);

    expect(useSessionPanel.getState().open).toBe(false);
  });

  it("does not open the session panel on connect when it would not fit beside the open docks", async () => {
    setViewportWidth(900);
    useUi.setState({ leftOpen: true, rightOpen: true, leftWidth: 248, rightWidth: 352 });

    await connectAsset(ASSET);

    expect(useSessionPanel.getState().open).toBe(false);
  });

  it("an explicit open after a hand close re-enables opening on connect", async () => {
    setViewportWidth(1280);
    useUi.setState({ leftOpen: false, rightOpen: false });
    showSessionPanel(false);
    showSessionPanel(true);
    useSessionPanel.getState().setOpen(false);

    await connectAsset(ASSET);

    expect(useSessionPanel.getState().open).toBe(true);
  });

  it("opens the session side panel with the host's files after connecting, leaving the left dock alone", async () => {
    setViewportWidth(1280);
    useUi.setState({ leftOpen: false, leftMode: "assets" });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(useSessionPanel.getState().open).toBe(true);
    expect(s.leftOpen).toBe(false);
    expect(s.leftMode).toBe("assets");
    expect(s.connectFocusRevision).toBe(1);
  });
});

describe("Layout preferences after reloading", () => {
  it("does not rewrite persisted widths on narrow screens; widths remain after reloading the store", async () => {
    setViewportWidth(500);
    useUi.getState().setLeftWidth(300);

    await connectAsset(ASSET);
    expect(localStorage.getItem("nexterm.layout.v1")).toBe(
      JSON.stringify({ leftWidth: 300, rightWidth: 352, rightOpen: true }),
    );

    vi.resetModules();
    const fresh = await import("../../app/store");
    expect(fresh.useUi.getState().leftWidth).toBe(300);
  });
});
