/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, deferred, flush, flushUntil, mount, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  kill: vi.fn(),
  portsList: vi.fn(),
  forwardAndOpen: vi.fn(),
  confirmDialog: vi.fn(),
}));

vi.mock("../../ipc/hostProcessesApi", () => ({ hostProcessesApi: { list: mocks.list, kill: mocks.kill } }));
vi.mock("../../ipc/hostPortsApi", () => ({ hostPortsApi: { list: mocks.portsList, connectivityTest: vi.fn() } }));
vi.mock("../../features/preview/forwardAndOpen", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/preview/forwardAndOpen")>();
  return { ...actual, forwardAndOpen: mocks.forwardAndOpen };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, confirmDialog: mocks.confirmDialog };
});

import { ProcessesSection } from "../../features/hosts/ProcessesSection";
import { useUi, type Workspace } from "../../app/store";

let mounted: MountedView | undefined;

const workspace = {
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

const proc = (pid: number, name: string, extra: Record<string, unknown> = {}) => ({
  pid,
  ppid: 1,
  user: "root",
  cpu: 1,
  mem: 1,
  rssKiB: 2048,
  elapsedSec: 90,
  name,
  command: `/usr/bin/${name} --flag`,
  ...extra,
});

function mountSection(ws: Workspace | null = workspace, visible = true) {
  mounted = mountWithClient(createElement(ProcessesSection, { workspace: ws ?? undefined, visible, hostName: "web-01" }));
  return mounted;
}

const q = (view: MountedView, sel: string) => view.container.querySelector<HTMLElement>(sel);

async function refreshed(view: MountedView) {
  click(q(view, 'button[aria-label="刷新进程"]')!);
  await flushUntil(() => view.container.querySelector("tbody") !== null);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.list.mockResolvedValue({
    processes: [proc(100, "nginx", { cpu: 5 }), proc(200, "redis")],
    platform: "posix",
    format: "ps",
  });
  mocks.portsList.mockResolvedValue({
    listeners: [{ proto: "tcp", address: "0.0.0.0", port: 8080, pid: 100, process: "nginx" }],
    format: "ss",
    platform: "posix",
  });
  mocks.kill.mockImplementation(async (a: { dryRun?: boolean }) =>
    a.dryRun
      ? { command: "kill -TERM 100", signal: "TERM", ran: false, ok: false, output: "", durationMs: 0 }
      : { command: "kill -TERM 100", signal: "TERM", ran: true, ok: true, output: "", durationMs: 3 },
  );
  useUi.setState({
    sessions: [{ id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
  } as never);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("ProcessesSection", () => {
  it("asks to connect without a workspace and sends no request", () => {
    const view = mountSection(null);
    expect(view.container.textContent).toContain("打开并连接工作区");
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("reads only after refresh and associates listening ports with their process", async () => {
    const view = mountSection();
    expect(mocks.list).not.toHaveBeenCalled();
    await refreshed(view);
    await flushUntil(() => q(view, 'button[aria-label="转发并打开 8080"]') !== null);
    expect(mocks.list).toHaveBeenCalledWith({ sessionId: "s1" });
    expect(mocks.portsList).toHaveBeenCalledWith({ sessionId: "s1", processes: true });
    expect(q(view, 'tr[data-pid="100"]')!.textContent).toContain("8080");
    click(q(view, 'button[aria-label="转发并打开 8080"]')!);
    expect(mocks.forwardAndOpen).toHaveBeenCalledWith({ sessionId: "s1", host: expect.any(String), port: 8080 });
  });

  it("keeps the process list when ports fail and says so", async () => {
    mocks.portsList.mockRejectedValue(new Error("no ss"));
    const view = mountSection();
    await refreshed(view);
    await flushUntil(() => view.container.textContent!.includes("未能读取端口"));
    expect(q(view, 'tr[data-pid="100"]')).not.toBeNull();
  });

  it("filters by search text", async () => {
    const view = mountSection();
    await refreshed(view);
    setInputValue(q(view, 'input[aria-label="搜索进程"]') as HTMLInputElement, "REDIS");
    expect(q(view, 'tr[data-pid="100"]')).toBeNull();
    expect(q(view, 'tr[data-pid="200"]')).not.toBeNull();
  });

  it("notes columns the host did not provide", async () => {
    mocks.list.mockResolvedValue({ processes: [proc(100, "nginx")], platform: "posix", format: "ps", partial: ["cpu", "user"] });
    const view = mountSection();
    await refreshed(view);
    expect(view.container.textContent).toContain("该主机未提供：CPU、用户");
  });

  it("previews the exact command, then runs after confirmation", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    const view = mountSection();
    await refreshed(view);
    click(q(view, 'button[aria-label="结束进程 100"]')!);
    await flushUntil(() => mocks.kill.mock.calls.length === 2);
    const text = mocks.confirmDialog.mock.calls[0][0] as string;
    expect(text).toContain("kill -TERM 100");
    expect(text).toContain("nginx");
    expect(text).toContain("100");
    expect(text).toContain("web-01");
    expect(mocks.kill.mock.calls[0][0]).toMatchObject({ pid: 100, name: "nginx", dryRun: true });
    expect(mocks.kill.mock.calls[1][0].dryRun).toBeFalsy();
    expect(mocks.kill.mock.calls[1][0].force).toBeFalsy();
    await flushUntil(() => mocks.list.mock.calls.length === 2);
  });

  it("does not run when cancelled", async () => {
    mocks.confirmDialog.mockResolvedValue(false);
    const view = mountSection();
    await refreshed(view);
    click(q(view, 'button[aria-label="结束进程 100"]')!);
    await flushUntil(() => mocks.confirmDialog.mock.calls.length === 1);
    await flush();
    expect(mocks.kill).toHaveBeenCalledTimes(1);
  });

  it("force kill sends force and warns that cleanup is skipped", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    const view = mountSection();
    await refreshed(view);
    click(q(view, 'button[aria-label="强制结束进程 100"]')!);
    await flushUntil(() => mocks.kill.mock.calls.length === 2);
    expect(mocks.kill.mock.calls[0][0].force).toBe(true);
    expect(mocks.kill.mock.calls[1][0].force).toBe(true);
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain("清理");
  });

  it("shows the audit failure and hides actions for pid 1", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    mocks.list.mockResolvedValue({ processes: [proc(1, "init", { ppid: 0 }), proc(100, "nginx")], platform: "posix", format: "ps" });
    mocks.kill.mockImplementation(async (a: { dryRun?: boolean }) =>
      a.dryRun
        ? { command: "kill -TERM 100", signal: "TERM", ran: false, ok: false, output: "", durationMs: 0 }
        : { command: "kill -TERM 100", signal: "TERM", ran: true, ok: true, output: "", durationMs: 3, auditError: "disk full" },
    );
    const view = mountSection();
    await refreshed(view);
    expect(q(view, 'button[aria-label="结束进程 1"]')).toBeNull();
    click(q(view, 'button[aria-label="结束进程 100"]')!);
    await flushUntil(() => view.container.textContent!.includes("已执行，但审计记录写入失败：disk full"));
  });

  it("auto refresh is off by default, ticks every 5 seconds when on and stops when off", async () => {
    vi.useFakeTimers();
    const view = mountSection();
    const toggle = q(view, 'input[aria-label="自动刷新（5 秒）"]') as HTMLInputElement;
    expect(toggle.checked).toBe(false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20000);
    });
    expect(mocks.list).not.toHaveBeenCalled();
    click(toggle);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
    click(toggle);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20000);
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });
});

function setHidden(value: boolean) {
  Object.defineProperty(document, "hidden", { configurable: true, get: () => value });
}

const tick = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

describe("ProcessesSection review fixes", () => {
  afterEach(() => {
    delete (document as unknown as Record<string, unknown>).hidden;
  });

  it("sends argName, ppid and started on kill but shows the display name", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    mocks.list.mockResolvedValue({
      processes: [proc(100, "nginx: master", { argName: "nginx", ppid: 7, started: "Mon Jan  1 00:00:00 2024" })],
      platform: "posix",
      format: "ps",
    });
    const view = mountSection();
    await refreshed(view);
    click(q(view, 'button[aria-label="结束进程 100"]')!);
    await flushUntil(() => mocks.kill.mock.calls.length === 2);
    for (const call of mocks.kill.mock.calls) {
      expect(call[0]).toMatchObject({ pid: 100, name: "nginx", ppid: 7, started: "Mon Jan  1 00:00:00 2024" });
    }
    expect(mocks.confirmDialog.mock.calls[0][0]).toContain("nginx: master");
  });

  it("reports skipped rows", async () => {
    mocks.list.mockResolvedValue({ processes: [proc(100, "nginx")], platform: "posix", format: "ps", skippedRows: 3 });
    const view = mountSection();
    await refreshed(view);
    expect(view.container.textContent).toContain("有 3 行无法解析，已跳过");
  });

  it("shows a dash for elapsed time and names it as missing when partial has elapsed", async () => {
    mocks.list.mockResolvedValue({ processes: [proc(100, "nginx")], platform: "posix", format: "ps-busybox-min", partial: ["elapsed"] });
    const view = mountSection();
    await refreshed(view);
    expect(view.container.textContent).toContain("该主机未提供：运行时长");
    expect(q(view, 'tr[data-pid="100"]')!.textContent).not.toContain("1分30秒");
    expect(q(view, 'tr[data-pid="100"]')!.querySelectorAll("td")[5].textContent).toBe("-");
  });

  it("hides kill buttons for critical Windows processes only", async () => {
    mocks.list.mockResolvedValue({
      processes: [
        proc(4, "System"),
        proc(300, "LSASS.EXE"),
        proc(310, "csrss"),
        proc(400, "notepad.exe"),
      ],
      platform: "windows",
      format: "powershell",
    });
    const view = mountSection();
    await refreshed(view);
    for (const pid of [4, 300, 310]) {
      expect(q(view, `button[aria-label="结束进程 ${pid}"]`)).toBeNull();
      expect(q(view, `button[aria-label="强制结束进程 ${pid}"]`)).toBeNull();
    }
    expect(q(view, 'button[aria-label="结束进程 400"]')).not.toBeNull();
    expect(q(view, 'button[aria-label="强制结束进程 400"]')).not.toBeNull();
  });

  it("does not hide posix processes that share a Windows critical name", async () => {
    mocks.list.mockResolvedValue({ processes: [proc(4, "smss"), proc(5, "lsass")], platform: "posix", format: "ps" });
    const view = mountSection();
    await refreshed(view);
    expect(q(view, 'button[aria-label="结束进程 4"]')).not.toBeNull();
    expect(q(view, 'button[aria-label="结束进程 5"]')).not.toBeNull();
  });

  it("explains the CPU column basis in a tooltip", async () => {
    const view = mountSection();
    await refreshed(view);
    const th = Array.from(view.container.querySelectorAll("th")).find((h) => h.textContent === "CPU%")!;
    expect(th.getAttribute("title")).toContain("100");
    expect(th.getAttribute("title")).toContain("Windows");
  });

  it("shows the error text and does not refresh when kill rejects", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    mocks.kill.mockImplementation(async (a: { dryRun?: boolean }) => {
      if (a.dryRun) return { command: "kill -TERM 100", signal: "TERM", ran: false, ok: false, output: "", durationMs: 0 };
      throw new Error("boom-kill");
    });
    const view = mountSection();
    await refreshed(view);
    click(q(view, 'button[aria-label="结束进程 100"]')!);
    await flushUntil(() => view.container.textContent!.includes("boom-kill"));
    await flush();
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });

  it("queues one refresh after a kill that lands during a running refresh", async () => {
    mocks.confirmDialog.mockResolvedValue(true);
    const view = mountSection();
    await refreshed(view);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    const slow = deferred<unknown>();
    mocks.list.mockImplementationOnce(() => slow.promise);
    click(q(view, 'button[aria-label="刷新进程"]')!);
    await flushUntil(() => mocks.list.mock.calls.length === 2);
    click(q(view, 'button[aria-label="结束进程 100"]')!);
    await flushUntil(() => mocks.kill.mock.calls.length === 2);
    await flush();
    expect(mocks.list).toHaveBeenCalledTimes(2);
    slow.resolve({ processes: [proc(100, "nginx")], platform: "posix", format: "ps" });
    await flushUntil(() => mocks.list.mock.calls.length === 3);
    await flush();
    expect(mocks.list).toHaveBeenCalledTimes(3);
  });

  it("pauses auto refresh while the document is hidden and resumes when visible", async () => {
    vi.useFakeTimers();
    const view = mountSection();
    click(q(view, 'input[aria-label="自动刷新（5 秒）"]')!);
    await tick(5000);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    setHidden(true);
    await tick(20000);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    setHidden(false);
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await tick(5000);
    expect(mocks.list.mock.calls.length).toBeGreaterThan(1);
  });

  it("stops auto refresh when visible becomes false and on unmount", async () => {
    vi.useFakeTimers();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const node = (visible: boolean) =>
      createElement(QueryClientProvider, { client }, createElement(ProcessesSection, { workspace, visible, hostName: "web-01" }));
    const view = mount(node(true));
    mounted = view;
    click(q(view, 'input[aria-label="自动刷新（5 秒）"]')!);
    await tick(5000);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    act(() => view.root.render(node(false)));
    await tick(20000);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    act(() => view.root.render(node(true)));
    await tick(5000);
    expect(mocks.list).toHaveBeenCalledTimes(2);
    view.unmount();
    mounted = undefined;
    await tick(20000);
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });
});

describe("ProcessesSection paging and links", () => {
  const many = Array.from({ length: 130 }, (_, index) => proc(1000 + index, index === 120 ? "postgres" : `worker-${index}`, { cpu: 200 - index }));

  function mountLinked(props: Record<string, unknown>) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const render = (next: Record<string, unknown>) =>
      createElement(QueryClientProvider, { client }, createElement(ProcessesSection, { workspace, visible: true, hostName: "web-01", ...next }));
    const view = mount(render(props));
    return { view, rerender: (next: Record<string, unknown>) => act(() => view.root.render(render(next))) };
  }

  it("pages a long process list instead of cutting it off", async () => {
    mocks.list.mockResolvedValue({ processes: many, platform: "posix", format: "ps" });
    const view = mountSection();
    mounted = view;
    await refreshed(view);
    expect(view.container.querySelectorAll("tbody tr")).toHaveLength(50);
    expect(view.container.textContent).toContain("第 1 / 3 页");
    expect(q(view, "[data-list-count]")!.textContent).toBe("共 130 个");
    click([...view.container.querySelectorAll("button")].find((button) => button.textContent === "下一页")!);
    await flushUntil(() => view.container.textContent!.includes("第 2 / 3 页"));
    expect(q(view, 'tr[data-pid="1050"]')).not.toBeNull();
  });

  it("shows a requested PID: reads the list first, clears a search that hides it, turns to its page and marks it", async () => {
    mocks.list.mockResolvedValue({ processes: many, platform: "posix", format: "ps" });
    const linked = mountLinked({ focus: null });
    mounted = linked.view;
    linked.rerender({ focus: { value: 1125, nonce: 1 } });
    await flushUntil(() => linked.view.container.querySelector('tr[data-pid="1125"]') !== null);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(linked.view.container.querySelector('tr[data-pid="1125"]')!.className).toContain("is-focused");
    expect(linked.view.container.textContent).toContain("第 3 / 3 页");

    setInputValue(linked.view.container.querySelector<HTMLInputElement>('input[aria-label="搜索进程"]')!, "postgres");
    await flushUntil(() => linked.view.container.querySelectorAll("tbody tr").length === 1);
    linked.rerender({ focus: { value: 1001, nonce: 2 } });
    await flushUntil(() => linked.view.container.querySelector('tr[data-pid="1001"]') !== null);
    expect(linked.view.container.querySelector<HTMLInputElement>('input[aria-label="搜索进程"]')!.value).toBe("");
    expect(linked.view.container.querySelector('tr[data-pid="1001"]')!.className).toContain("is-focused");
  });

  it("opens a listening port in the ports list", async () => {
    const onShowPort = vi.fn();
    const linked = mountLinked({ onShowPort });
    mounted = linked.view;
    await refreshed(linked.view);
    await flushUntil(() => linked.view.container.querySelector('button[aria-label="查看端口 8080"]') !== null);
    click(linked.view.container.querySelector('button[aria-label="查看端口 8080"]')!);
    expect(onShowPort).toHaveBeenCalledWith(8080);
  });
});
