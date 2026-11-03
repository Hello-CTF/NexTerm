/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

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
