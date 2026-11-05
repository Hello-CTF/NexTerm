/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

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

  it("explains that a non-root user only sees its own processes", () => {
    const view = mountSection(workspace);
    expect(view.container.textContent).toContain("非 root");
    expect(view.container.textContent).toContain("进程列可能为空");
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
  it("searches and pages a long port list in port order", async () => {
    const listeners = Array.from({ length: 120 }, (_, index) => ({
      proto: "tcp" as const,
      address: index % 2 ? "127.0.0.1" : "0.0.0.0",
      port: 10120 - index,
      pid: 1000 + index,
      process: index === 7 ? "postgres" : "worker",
    }));
    mocks.list.mockResolvedValue({ listeners, format: "ss", platform: "posix" });
    const view = mountSection(workspace);
    click(view.container.querySelector('button[aria-label="刷新端口"]')!);
    await flushUntil(() => view.container.querySelector('input[aria-label="搜索端口"]') !== null);
    const rows = () => [...view.container.querySelectorAll("tbody tr")].map((row) => row.querySelector("td")?.textContent);

    expect(view.container.querySelector('[data-list-count]')?.textContent).toBe("共 120 个");
    expect(rows()).toHaveLength(50);
    expect(rows()[0]).toBe("10001");
    expect(view.container.textContent).toContain("第 1 / 3 页");
    click([...view.container.querySelectorAll("button")].find((button) => button.textContent === "下一页")!);
    await flushUntil(() => view.container.textContent!.includes("第 2 / 3 页"));
    expect(rows()[0]).toBe("10051");

    setInputValue(view.container.querySelector<HTMLInputElement>('input[aria-label="搜索端口"]')!, "postgres");
    await flushUntil(() => rows().length === 1);
    expect(rows()[0]).toBe("10113");
    expect(view.container.querySelector('[data-list-count]')?.textContent).toBe("匹配 1 / 120 个");
    expect(view.container.textContent).not.toContain("下一页");

    setInputValue(view.container.querySelector<HTMLInputElement>('input[aria-label="搜索端口"]')!, "127.0.0.1 1011");
    // Every term must match some field: the PID 1011 of port 10109 counts as well as the ports 1011x.
    await flushUntil(() => rows().length === 6);
    expect(rows()).toEqual(["10109", "10111", "10113", "10115", "10117", "10119"]);

    setInputValue(view.container.querySelector<HTMLInputElement>('input[aria-label="搜索端口"]')!, "no-such-port");
    await flushUntil(() => view.container.textContent!.includes("没有匹配的端口"));
  });

  it("filters exposed and loopback-only ports and opens the listening process", async () => {
    mocks.list.mockResolvedValue({
      listeners: [
        { proto: "tcp", address: "0.0.0.0", port: 80, pid: 10, process: "nginx" },
        { proto: "tcp", address: "127.0.0.1", port: 5432, pid: 20, process: "postgres" },
        { proto: "tcp6", address: "::1", port: 6379, pid: 30, process: "redis" },
        { proto: "tcp6", address: "::", port: 22, pid: 40, process: "sshd" },
      ],
      format: "ss",
      platform: "posix",
    });
    const onShowProcess = vi.fn();
    mounted = mountWithClient(createElement(PortsSection, { workspace, visible: true, hostName: "web-01", onShowProcess }));
    const view = mounted;
    click(view.container.querySelector('button[aria-label="刷新端口"]')!);
    await flushUntil(() => view.container.querySelector('[aria-label="端口范围"]') !== null);
    const ports = () => [...view.container.querySelectorAll("tbody tr")].map((row) => row.getAttribute("data-port"));
    const scope = (label: string) => [...view.container.querySelectorAll<HTMLButtonElement>('[aria-label="端口范围"] button')].find((b) => b.textContent!.startsWith(label))!;

    expect(scope("对外").textContent).toBe("对外 2");
    click(scope("对外"));
    await flushUntil(() => ports().length === 2);
    expect(ports()).toEqual(["22", "80"]);
    click(scope("仅本机"));
    await flushUntil(() => ports().join() === "5432,6379");

    click(view.container.querySelector('button[aria-label="查看进程 20"]')!);
    expect(onShowProcess).toHaveBeenCalledWith(20);
  });

  it("shows a requested port even when a filter or another page hides it", async () => {
    const listeners = Array.from({ length: 120 }, (_, index) => ({
      proto: "tcp" as const, address: index === 100 ? "0.0.0.0" : "127.0.0.1", port: 20000 + index,
    }));
    mocks.list.mockResolvedValue({ listeners, format: "ss", platform: "posix" });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const render = (focus: unknown) =>
      createElement(QueryClientProvider, { client }, createElement(PortsSection, { workspace, visible: true, hostName: "web-01", focus: focus as never }));
    const view = mount(render(null));
    mounted = view;
    click(view.container.querySelector('button[aria-label="刷新端口"]')!);
    await flushUntil(() => view.container.querySelector('[aria-label="端口范围"]') !== null);
    click([...view.container.querySelectorAll<HTMLButtonElement>('[aria-label="端口范围"] button')].find((b) => b.textContent!.startsWith("对外"))!);
    await flushUntil(() => view.container.querySelectorAll("tbody tr").length === 1);

    act(() => view.root.render(render({ value: 20010, nonce: 1 })));
    await flushUntil(() => view.container.querySelector('tr[data-port="20010"]') !== null);
    expect(view.container.querySelector('tr[data-port="20010"]')!.className).toContain("is-focused");
    act(() => view.root.render(render({ value: 20110, nonce: 2 })));
    await flushUntil(() => view.container.querySelector('tr[data-port="20110"]') !== null);
    expect(view.container.textContent).toContain("第 3 / 3 页");
  });
});
