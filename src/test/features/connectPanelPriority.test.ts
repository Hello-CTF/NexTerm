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
  it("shows the host workspace after connecting and leaves the docks alone", async () => {
    setViewportWidth(1280);
    useUi.setState({ leftOpen: false, leftMode: "assets", rightOpen: true });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(s.leftOpen).toBe(false);
    expect(s.leftMode).toBe("assets");
    expect(s.rightOpen).toBe(true);
    expect(s.connectFocusRevision).toBe(1);
    expect(s.activeWorkspaceId).toBe(s.workspaces.find((w) => w.sessionId === SESSION.id)?.id);
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
