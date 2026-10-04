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
  name: "测试机",
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 0,
};

const ASSET = { id: "a1", name: "测试机", kind: "ssh" };

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

describe("窄屏连接后优先终端", () => {
  it("保留用户显式关闭的左栏与面板模式，不写入隐藏偏好", async () => {
    setViewportWidth(500);
    useUi.setState({ leftOpen: false, leftMode: "assets", rightOpen: false });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(s.leftOpen).toBe(false);
    expect(s.leftMode).toBe("assets");
    expect(s.rightOpen).toBe(false);
    expect(s.connectFocusRevision).toBe(1);

    const ws = s.workspaces.find((w) => w.sessionId === SESSION.id);
    const pane = ws?.panes[0];
    const terminal = pane?.tabs.find((t) => t.kind === "terminal");
    expect(terminal).toBeDefined();
    expect(pane?.activeTabId).toBe(terminal?.id);
  });

  it("用户显式打开的左栏保持原模式，不被连接流程改成文件树", async () => {
    setViewportWidth(500);
    useUi.setState({ leftOpen: true, leftMode: "credentials" });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(s.leftOpen).toBe(true);
    expect(s.leftMode).toBe("credentials");
  });

  it("持久化布局中的面板偏好与宽度不被窄屏连接改动", async () => {
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

describe("宽屏连接保持既有布局行为", () => {
  it("连接后仍自动展开左栏文件树", async () => {
    setViewportWidth(1280);
    useUi.setState({ leftOpen: false, leftMode: "assets" });

    await connectAsset(ASSET);

    const s = useUi.getState();
    expect(s.leftOpen).toBe(true);
    expect(s.leftMode).toBe("files");
    expect(s.connectFocusRevision).toBe(1);
  });
});

describe("布局偏好重载后保留", () => {
  it("窄屏连接不改写已持久化宽度，重载 store 后宽度仍在", async () => {
    setViewportWidth(500);
    useUi.getState().setLeftWidth(300);

    await connectAsset(ASSET);
    expect(localStorage.getItem("nexterm.layout.v1")).toBe(
      JSON.stringify({ leftWidth: 300, rightWidth: 352 }),
    );

    vi.resetModules();
    const fresh = await import("../../app/store");
    expect(fresh.useUi.getState().leftWidth).toBe(300);
  });
});
