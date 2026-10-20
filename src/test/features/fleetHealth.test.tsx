/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { projectFocusForGroup } from "../../app/projectFocus";
import { useUi } from "../../app/store";
import type { Asset, AssetGroup, SessionInfo } from "../../ipc/commands";
import type { ExecBatchHealth, ExecBatchHealthRequest } from "../../ipc/execBatchApi";
import {
  click,
  clickButton,
  deferred,
  flushUntil,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  groupList: vi.fn(),
  healthProbe: vi.fn(),
  cancel: vi.fn(),
  createId: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, list: mocks.assetList, groupList: mocks.groupList },
  };
});

vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return {
    ...actual,
    createExecBatchId: mocks.createId,
    execBatchApi: { ...actual.execBatchApi, healthProbe: mocks.healthProbe, healthCancel: mocks.cancel },
  };
});

import { FleetHealthPanel } from "../../features/fleet/FleetHealthPanel";

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: `Project ${id}`, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id,
    groupId,
    kind: "ssh",
    name: id,
    host: `${id}.example.test`,
    port: 22,
    username: "root",
    authKind: "agent",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
  };
}

const groups = [group("p1"), group("child", "p1"), group("p2")];
const assets = [asset("healthy", "p1"), asset("offline", "child"), asset("foreign", "p2")];
const sessions: SessionInfo[] = [
  { id: "session-healthy", assetId: "healthy", name: "healthy", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
  { id: "session-foreign", assetId: "foreign", name: "foreign", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
];

function healthResponse(batchId: string): ExecBatchHealth {
  return {
    batchId,
    results: [
      {
        assetId: "healthy",
        sessionId: "session-healthy",
        reachable: true,
        latencyMs: 12,
        status: "success",
        uptimeSeconds: 90061,
        load1: 0.1,
        load5: 0.2,
        load15: 0.3,
        diskUsedPercent: 42,
        memoryUsedPercent: 70,
        dockerReclaimableBytes: 1_000_000_000,
        lastBatch: { batchId: "batch-old", status: "exit_error", startedAt: 1000, finishedAt: 2000 },
      },
      {
        assetId: "offline",
        reachable: false,
        latencyMs: 5000,
        status: "transport_error",
        error: "connection refused",
      },
    ],
  };
}

let mounted: MountedView | undefined;
let client: QueryClient | undefined;
let nextId = 0;

function renderPanel(visible: boolean): void {
  if (!mounted || !client) throw new Error("panel is not mounted");
  const currentClient = client;
  act(() => {
    mounted!.root.render(
      createElement(
        QueryClientProvider,
        { client: currentClient },
        createElement(FleetHealthPanel, { sessions, visible }),
      ),
    );
  });
}

function mountPanel(visible = true): void {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const currentClient = client;
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client: currentClient },
      createElement(FleetHealthPanel, { sessions, visible }),
    ),
  );
}

async function waitForHosts(): Promise<void> {
  await flushUntil(() => {
    const text = mounted?.container.textContent ?? "";
    return text.includes("healthy") && text.includes("offline") && !text.includes("正在读取项目主机");
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
  window.localStorage.clear();
  document.body.replaceChildren();
  nextId = 0;
  mocks.createId.mockImplementation(() => `health-${++nextId}`);
  mocks.assetList.mockResolvedValue(assets);
  mocks.groupList.mockResolvedValue(groups);
  mocks.healthProbe.mockImplementation((request: ExecBatchHealthRequest) => Promise.resolve(healthResponse(request.batchId)));
  mocks.cancel.mockResolvedValue({ canceled: true });
  useUi.setState({ activeProject: projectFocusForGroup("p1"), projectInventory: null });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  client?.clear();
  client = undefined;
  vi.useRealTimers();
});

describe("FleetHealthPanel", () => {
  it("is manual by default, filters the active project, and keeps failures and empty history visible", async () => {
    mountPanel();
    await waitForHosts();
    expect(mocks.healthProbe).not.toHaveBeenCalled();
    expect(mounted!.container.textContent).not.toContain("foreign.example.test");

    clickButton(mounted!.container, "刷新");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("42.0%"));

    expect(mocks.healthProbe).toHaveBeenCalledTimes(1);
    expect(mocks.healthProbe).toHaveBeenCalledWith({
      batchId: "health-1",
      targets: [{ assetId: "healthy", sessionId: "session-healthy" }, { assetId: "offline" }],
      timeoutMs: 30_000,
      maxConcurrent: 4,
    });
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("0.10 / 0.20 / 0.30");
    expect(text).toContain("70.0%");
    expect(text).toContain("1.00 GB");
    expect(text).toContain("非零退出");
    expect(text).toContain("connection refused");
    expect(text).toContain("暂无");
  });

  it("coalesces manual refresh clicks while a probe is in flight", async () => {
    const pending = deferred<ExecBatchHealth>();
    mocks.healthProbe.mockReturnValue(pending.promise);
    mountPanel();
    await waitForHosts();

    const button = clickButton(mounted!.container, "刷新");
    click(button);
    await flushUntil(() => mocks.healthProbe.mock.calls.length === 1);
    expect(mocks.healthProbe).toHaveBeenCalledTimes(1);

    await act(async () => pending.resolve(healthResponse("health-1")));
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("42.0%"));
  });

  it("polls only after project opt-in and stops scheduling when the tab is hidden", async () => {
    mountPanel();
    await waitForHosts();
    vi.useFakeTimers();

    const interval = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="自动刷新间隔（分钟）"]');
    if (!interval) throw new Error("interval input not found");
    setInputValue(interval, "0");
    expect(interval.value).toBe("1");
    const toggle = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="按项目自动刷新"]');
    if (!toggle) throw new Error("auto polling toggle not found");
    click(toggle);
    await waitFor(() => expect(mocks.healthProbe).toHaveBeenCalledTimes(1));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    await waitFor(() => expect(mocks.healthProbe).toHaveBeenCalledTimes(2));
    renderPanel(false);
    await act(async () => vi.advanceTimersByTimeAsync(300_000));
    expect(mocks.healthProbe).toHaveBeenCalledTimes(2);
  });

  it("cancels an in-flight probe on hide and ignores the stale response", async () => {
    const pending = deferred<ExecBatchHealth>();
    mocks.healthProbe.mockReturnValue(pending.promise);
    mountPanel();
    await waitForHosts();
    vi.useFakeTimers();

    const toggle = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="按项目自动刷新"]');
    if (!toggle) throw new Error("auto polling toggle not found");
    click(toggle);
    await waitFor(() => expect(mocks.healthProbe).toHaveBeenCalledTimes(1));
    renderPanel(false);
    await waitFor(() => expect(mocks.cancel).toHaveBeenCalledWith("health-1"));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(300_000);
    });
    expect(mocks.healthProbe).toHaveBeenCalledTimes(1);
    await act(async () => {
      pending.resolve(healthResponse("health-1"));
      await Promise.resolve();
    });
    const text = mounted!.container.textContent ?? "";
    expect(text).not.toContain("42.0%");
    expect(text).not.toContain("上次刷新");
  });

  it("cancels the old project request and does not apply it after a project switch", async () => {
    const pending = deferred<ExecBatchHealth>();
    mocks.healthProbe.mockReturnValue(pending.promise);
    mountPanel();
    await waitForHosts();
    vi.useFakeTimers();

    const toggle = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="按项目自动刷新"]');
    if (!toggle) throw new Error("auto polling toggle not found");
    click(toggle);
    await waitFor(() => expect(mocks.healthProbe).toHaveBeenCalledTimes(1));
    act(() => useUi.setState({ activeProject: projectFocusForGroup("p2") }));
    await waitFor(() => expect(mocks.cancel).toHaveBeenCalledWith("health-1"));
    await waitFor(() => expect(mounted!.container.textContent).toContain("foreign.example.test"));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(300_000);
    });
    expect(mocks.healthProbe).toHaveBeenCalledTimes(1);
    await act(async () => {
      pending.resolve(healthResponse("health-1"));
      await Promise.resolve();
    });
    const text = mounted!.container.textContent ?? "";
    expect(text).not.toContain("42.0%");
    expect(text).not.toContain("上次刷新");
    expect(text).not.toContain("healthy.example.test");
  });

  it("cancels an in-flight opt-in probe on unmount and does not poll afterward", async () => {
    const pending = deferred<ExecBatchHealth>();
    mocks.healthProbe.mockReturnValue(pending.promise);
    mountPanel();
    await waitForHosts();
    vi.useFakeTimers();

    const toggle = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="按项目自动刷新"]');
    if (!toggle) throw new Error("auto polling toggle not found");
    click(toggle);
    await flushUntil(() => mocks.healthProbe.mock.calls.length === 1);
    mounted!.unmount();
    mounted = undefined;

    expect(mocks.cancel).toHaveBeenCalledWith("health-1");
    await act(async () => vi.advanceTimersByTimeAsync(300_000));
    expect(mocks.healthProbe).toHaveBeenCalledTimes(1);
    await act(async () => pending.resolve(healthResponse("health-1")));
  });
});
