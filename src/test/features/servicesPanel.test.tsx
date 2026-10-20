/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import type { ExecBatchResult } from "../../ipc/execBatchApi";
import type { ServiceOperationBatch, ServiceUnit } from "../../ipc/servicesApi";
import type { SessionInfo } from "../../ipc/commands";
import type { TargetSetPickerTarget } from "../../features/execbatch/TargetSetPicker";
import {
  click,
  clickButton,
  flush,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  action: vi.fn(),
  find: vi.fn(),
  kill: vi.fn(),
  cancel: vi.fn(),
  createId: vi.fn(),
  logStart: vi.fn(),
  logCancel: vi.fn(),
  logRunId: vi.fn(),
  dispose: vi.fn(),
  onLogEvent: null as null | ((event: Record<string, unknown>) => void),
}));

vi.mock("../../ipc/servicesApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/servicesApi")>();
  return { ...actual, servicesApi: { list: mocks.list, action: mocks.action, find: mocks.find, kill: mocks.kill } };
});
vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return { ...actual, createExecBatchId: mocks.createId, execBatchApi: { ...actual.execBatchApi, cancel: mocks.cancel } };
});
vi.mock("../../ipc/logTailApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/logTailApi")>();
  return { ...actual, createLogTailRunId: mocks.logRunId, logTailApi: { ...actual.logTailApi, start: mocks.logStart, cancel: mocks.logCancel } };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createJsonChannel: vi.fn((onEvent: (event: Record<string, unknown>) => void) => {
      mocks.onLogEvent = onEvent;
      return { id: "journal-channel", onmessage: vi.fn(), toJSON: () => "journal-channel" };
    }),
    disposeChannel: mocks.dispose,
  };
});

import { ServicesPanel } from "../../features/services/ServicesPanel";

const sessions: SessionInfo[] = [
  { id: "s1", assetId: "a1", name: "web-one", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
  { id: "s2", assetId: "a2", name: "web-two", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
];
const targets: TargetSetPickerTarget[] = sessions.map((session) => ({
  id: session.id, label: session.name, sessionName: session.name, endpoint: null, tags: ["web"], workspaceNames: [], envCount: 0,
}));

function result(sessionId: string, patch: Partial<ExecBatchResult> = {}): ExecBatchResult {
  return {
    sessionId, sessionName: sessionId === "s1" ? "web-one" : "web-two", status: "success", stdout: "", stderr: "",
    exitCode: 0, durationMs: 1, truncated: false, ...patch,
  };
}

function batch(hosts: ServiceOperationBatch["hosts"]): ServiceOperationBatch {
  return { batchId: "batch-1", command: "systemctl", hosts };
}

const nginx: ServiceUnit = { name: "nginx.service", load: "loaded", active: "active", sub: "running", description: "HTTP server" };
const postgres: ServiceUnit = { name: "postgres.service", load: "loaded", active: "failed", sub: "failed", description: "Database" };

let mounted: MountedView | undefined;
let nextId = 0;

function panel(): MountedView {
  if (!mounted) throw new Error("panel not mounted");
  return mounted;
}

function target(id: string): HTMLInputElement {
  const input = panel().container.querySelector<HTMLInputElement>(`input[value="${id}"]`);
  if (!input) throw new Error(`target ${id} not found`);
  return input;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  nextId = 0;
  mocks.createId.mockImplementation(() => `batch-${++nextId}`);
  mocks.cancel.mockResolvedValue({ canceled: true });
  mocks.logRunId.mockReturnValue("journal-run");
  mocks.logStart.mockResolvedValue({ runId: "journal-run" });
  mocks.logCancel.mockResolvedValue({ runId: "journal-run", canceled: true });
  mocks.onLogEvent = null;
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function mountAndLoad(): Promise<void> {
  mocks.list.mockResolvedValue(batch([
    { result: result("s1"), systemd: "supported", units: [nginx, postgres] },
    { result: result("s2", { status: "exit_error", exitCode: 1, stderr: "sudo: a password is required" }), systemd: "unknown", failureKind: "sudo", units: [] },
  ]));
  mounted = mount(createElement(ServicesPanel, { targets, sessions }));
  click(target("s1"));
  click(target("s2"));
  clickButton(panel().container, "加载服务");
  await flushUntil(() => panel().container.textContent?.includes("nginx.service") ?? false);
}

describe("ServicesPanel", () => {
  it("loads and filters units while keeping sudo failures visible", async () => {
    await mountAndLoad();
    expect(mocks.list).toHaveBeenCalledWith(expect.objectContaining({ sessionIds: ["s1", "s2"], sudo: true }));
    expect(panel().container.textContent).toContain("sudo 不可用或需要密码");
    const filter = panel().container.querySelector<HTMLInputElement>('input[aria-label="过滤服务"]');
    if (!filter) throw new Error("service filter not found");
    setInputValue(filter, "HTTP");
    expect(panel().container.querySelector('tr[data-unit="nginx.service"]')).not.toBeNull();
    expect(panel().container.querySelector('tr[data-unit="postgres.service"]')).toBeNull();
  });

  it("runs a row action with the parsed unit and only its owning host", async () => {
    await mountAndLoad();
    mocks.action.mockResolvedValue(batch([{ result: result("s1"), systemd: "supported", units: [] }]));
    const row = panel().container.querySelector('tr[data-unit="nginx.service"]');
    if (!row) throw new Error("nginx row not found");
    clickButton(row, "重启");
    await flushUntil(() => mocks.action.mock.calls.length === 1);
    expect(mocks.action).toHaveBeenCalledWith(expect.objectContaining({
      sessionIds: ["s1"], unit: "nginx.service", action: "restart", sudo: true,
    }));
    await flushUntil(() => panel().container.textContent?.includes("服务操作逐目标结果") ?? false);
  });

  it("requires production confirmation for a cross-target restart and retries the same batch", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.action.mockRejectedValueOnce({
      code: "needs_confirm",
      detail: {
        kind: "exec_batch_production_confirmation", batchId: "batch-1", targetSetHash, commandHash,
        productionTargets: [{ sessionId: "s1", sessionName: "web-one", environment: "production" }],
      },
    }).mockResolvedValueOnce(batch([]));
    mounted = mount(createElement(ServicesPanel, { targets, sessions }));
    click(target("s1"));
    click(target("s2"));
    clickButton(panel().container, "应用到已选目标");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);
    expect(mocks.action).toHaveBeenCalledTimes(1);
    expect(mocks.action.mock.calls[0][0]).toMatchObject({ batchId: "batch-1", sessionIds: ["s1", "s2"], action: "restart" });
    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
    if (!dialog) throw new Error("production dialog not found");
    expect(dialog.textContent).toContain("systemctl restart -- nginx.service");
    click(dialog.querySelector<HTMLInputElement>('input[aria-label="确认生产环境风险"]')!);
    clickButton(dialog, "确认执行（1）");
    await flushUntil(() => mocks.action.mock.calls.length === 2);
    expect(mocks.action.mock.calls[1][0]).toMatchObject({
      batchId: "batch-1", productionConfirmation: { confirmed: true, targetSetHash, commandHash },
    });
    await flushUntil(() =>
      panel().container.textContent?.includes("服务操作逐目标结果") === true &&
      panel().container.querySelector<HTMLInputElement>('input[aria-label="批量服务 unit"]')?.disabled === false,
    );
  });

  it("marks hosts without systemd unsupported and opens journal through the R-1 stream API", async () => {
    mocks.list.mockResolvedValue(batch([
      { result: result("s1"), systemd: "supported", units: [nginx] },
      { result: result("s2", { status: "exit_error", exitCode: 127, stderr: "systemctl: command not found" }), systemd: "unsupported", failureKind: "systemd", units: [] },
    ]));
    mounted = mount(createElement(ServicesPanel, { targets, sessions }));
    click(target("s1"));
    click(target("s2"));
    clickButton(panel().container, "加载服务");
    await flushUntil(() => panel().container.textContent?.includes("unsupported：无 systemd") ?? false);
    const row = panel().container.querySelector('tr[data-unit="nginx.service"]');
    if (!row) throw new Error("nginx row not found");
    clickButton(row, "journal");
    await flushUntil(() => mocks.logStart.mock.calls.length === 1);
    expect(mocks.logStart.mock.calls[0][0]).toEqual({
      runId: "journal-run", maxConcurrent: 1,
      sources: [{ sourceId: "journal", sessionId: "s1", kind: "journal", value: "nginx.service" }],
    });
    act(() => {
      mocks.onLogEvent?.({ runId: "journal-run", sequence: 1, type: "line", sourceId: "journal", text: "nginx started", timeMs: 1 });
    });
    await flushUntil(() => panel().container.querySelector('[role="log"]')?.textContent?.includes("nginx started") ?? false);
    mounted.unmount();
    mounted = undefined;
    await flush();
    expect(mocks.logCancel).toHaveBeenCalledWith("journal-run");
  });
});
