/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ list: vi.fn(), restoreClosedTab: vi.fn() }));

vi.mock("../../ipc/hostPortsApi", () => ({ hostPortsApi: { list: mocks.list, connectivityTest: vi.fn() } }));
vi.mock("../../app/store/closedTabRestore", () => ({
  restoreClosedTab: mocks.restoreClosedTab,
  reconnectRestoredTab: vi.fn(),
}));

import { SessionSidebar } from "../../features/terminal/SessionSidebar";
import { useUi, type Workspace } from "../../app/store";
import { useClosedTabs, type ClosedTabEntry } from "../../app/closedTabs";

let mounted: MountedView | undefined;

const workspace = {
  id: "ws",
  kind: "session",
  title: "web-01",
  sessionId: "s1",
  assetId: "a1",
  assetKind: "ssh",
  panes: [
    {
      id: "p1",
      activeTabId: "t1",
      tabs: [
        { id: "t1", kind: "terminal", title: "term", sessionId: "s1", tabId: "k1", closable: true },
        { id: "f1", kind: "files", title: "files", sessionId: "s1", path: "/etc/nginx/nginx.conf", closable: true },
        { id: "l1", kind: "log", title: "log", sessionId: "s1", path: "/var/log/syslog", closable: true },
      ],
    },
  ],
  activePaneId: "p1",
  closable: true,
} as unknown as Workspace;

function closed(key: string, path: string, over: Partial<ClosedTabEntry> = {}): ClosedTabEntry {
  return { key, kind: "file", tabKind: "files", id: key, title: path, path, sessionId: "s1", assetId: "a1", seq: 1, ...over };
}

function mountSidebar(over: Partial<Parameters<typeof SessionSidebar>[0]> = {}) {
  mounted = mountWithClient(
    createElement(SessionSidebar, {
      workspaceId: "ws",
      storeTabId: "t1",
      kernelTabId: "k1",
      title: "term",
      visible: true,
      commandBlocks: createElement("div", { "data-testid": "blocks" }, "blocks"),
      onClose: vi.fn(),
      ...over,
    }),
  );
  return mounted.container;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.list.mockResolvedValue({ ports: [] });
  mocks.restoreClosedTab.mockResolvedValue(true);
  useUi.setState({
    workspaces: [workspace],
    sessions: [{ id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    aiPendingRefs: [],
    rightOpen: false,
  } as never);
  useClosedTabs.setState({
    stack: {
      accountId: "",
      generation: 0,
      nextSeq: 5,
      entries: [
        closed("k-same", "/home/me/a.txt"),
        closed("k-other", "/home/me/other-host.txt", { sessionId: "s9", assetId: "a9" }),
      ],
    },
  } as never);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

const byLabel = (c: HTMLElement, label: string) => c.querySelector<HTMLButtonElement>(`button[aria-label='${label}']`);

describe("SessionSidebar", () => {
  it("renders as a complementary landmark with the command blocks", () => {
    const c = mountSidebar();
    expect(c.querySelector("[role='complementary'][aria-label='会话侧栏']")).not.toBeNull();
    expect(c.querySelector("[data-testid='blocks']")).not.toBeNull();
  });

  it("lists open file tabs and recently closed files of the same host only", () => {
    const c = mountSidebar();
    expect(c.querySelector("[title='/etc/nginx/nginx.conf']")).not.toBeNull();
    expect(c.querySelector("[title='/var/log/syslog']")).not.toBeNull();
    expect(c.querySelector("[title='/home/me/a.txt']")).not.toBeNull();
    expect(c.querySelector("[title='/home/me/other-host.txt']")).toBeNull();
  });

  it("restores a recently closed file when clicked", () => {
    const c = mountSidebar();
    click(c.querySelector("[title='/home/me/a.txt']")!);
    expect(mocks.restoreClosedTab).toHaveBeenCalledWith("k-same");
  });

  it("activates an open file tab when clicked", () => {
    const setActiveTab = vi.fn();
    useUi.setState({ setActiveTab } as never);
    const c = mountSidebar();
    click(c.querySelector("[title='/etc/nginx/nginx.conf']")!);
    expect(setActiveTab).toHaveBeenCalledWith("f1");
  });

  it("queues the terminal as an AI reference and opens the AI panel", () => {
    const c = mountSidebar();
    click(byLabel(c, "在 AI 中引用此终端")!);
    const refs = useUi.getState().aiPendingRefs;
    expect(refs).toHaveLength(1);
    expect(refs[0]).toMatchObject({ kind: "tab", id: "tab:t1", refId: "k1" });
    expect(useUi.getState().rightOpen).toBe(true);
  });

  it("disables the AI reference without a kernel tab", () => {
    const c = mountSidebar({ kernelTabId: null });
    expect(byLabel(c, "在 AI 中引用此终端")!.disabled).toBe(true);
  });

  it("closes through the close button", () => {
    const onClose = vi.fn();
    const c = mountSidebar({ onClose });
    click(byLabel(c, "收起会话侧栏")!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("shows no ports or files for a Telnet host, which has no command channel", async () => {
    useUi.setState({ workspaces: [{ ...workspace, assetKind: "telnet" }] } as never);
    const c = mountSidebar();
    await Promise.resolve();
    expect(c.textContent).not.toContain("最近文件");
    expect(c.textContent).not.toContain("端口");
    expect(mocks.list).not.toHaveBeenCalled();
    expect(c.querySelector("[data-testid='blocks']")).not.toBeNull();
  });

  it("explains missing command record support", () => {
    const c = mountSidebar({ commandBlocks: null });
    expect(c.textContent).toContain("不支持命令记录");
  });
});
