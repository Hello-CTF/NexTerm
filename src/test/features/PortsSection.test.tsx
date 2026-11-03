/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ list: vi.fn(), connectivityTest: vi.fn(), forwardAndOpen: vi.fn() }));

vi.mock("../../ipc/hostPortsApi", () => ({ hostPortsApi: { list: mocks.list, connectivityTest: mocks.connectivityTest } }));
vi.mock("../../features/preview/forwardAndOpen", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/preview/forwardAndOpen")>();
  return { ...actual, forwardAndOpen: mocks.forwardAndOpen };
});

import { PortsSection } from "../../features/hosts/PortsSection";
import { useUi, type Workspace } from "../../app/store";

let mounted: MountedView | undefined;

const workspace: Workspace = {
  id: "ws",
  kind: "session",
  title: "web-01",
  sessionId: "s1",
  assetId: "a1",
  assetKind: "ssh",
  panes: [],
  activePaneId: "p",
  closable: true,
} as unknown as Workspace;

function mountSection(ws: Workspace | undefined, visible = true) {
  mounted = mountWithClient(createElement(PortsSection, { workspace: ws, visible, hostName: "web-01" }));
  return mounted;
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({
    sessions: [{ id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
  } as never);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("PortsSection", () => {
  it("asks to connect when there is no workspace and sends no request", () => {
    const view = mountSection(undefined);
    expect(view.container.textContent).toContain("打开并连接工作区");
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("does not read ports until refreshed, then lists them with processes", async () => {
    mocks.list.mockResolvedValue({
      listeners: [{ proto: "tcp", address: "0.0.0.0", port: 3000, pid: 42, process: "node" }],
      format: "ss",
      platform: "posix",
    });
    const view = mountSection(workspace);
    expect(mocks.list).not.toHaveBeenCalled();
    click(view.container.querySelector('button[aria-label="刷新端口"]')!);
    await flushUntil(() => view.container.textContent!.includes("node"));
    expect(mocks.list).toHaveBeenCalledWith({ sessionId: "s1", processes: true });
    expect(view.container.textContent).toContain("3000");
    expect(view.container.textContent).toContain("0.0.0.0");
    expect(view.container.textContent).toContain("42");
  });

  it("forwards and opens the clicked port", async () => {
    mocks.list.mockResolvedValue({
      listeners: [{ proto: "tcp", address: "0.0.0.0", port: 3000 }],
      format: "ss",
      platform: "posix",
    });
    const view = mountSection(workspace);
    click(view.container.querySelector('button[aria-label="刷新端口"]')!);
    await flushUntil(() => view.container.querySelector('button[aria-label="转发并打开 3000"]') !== null);
    click(view.container.querySelector('button[aria-label="转发并打开 3000"]')!);
    expect(mocks.forwardAndOpen).toHaveBeenCalledWith({ sessionId: "s1", host: "127.0.0.1", port: 3000 });
  });

  it("shows the error text when reading fails", async () => {
    mocks.list.mockRejectedValue(new Error("没有权限"));
    const view = mountSection(workspace);
    click(view.container.querySelector('button[aria-label="刷新端口"]')!);
    await flushUntil(() => view.container.textContent!.includes("未能读取端口"));
    expect(view.container.textContent).toContain("没有权限");
  });
});
