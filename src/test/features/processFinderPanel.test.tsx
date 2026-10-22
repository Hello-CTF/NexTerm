/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import type { SessionInfo } from "../../ipc/commands";
import type { ExecBatchResult } from "../../ipc/execBatchApi";
import type { FinderBatch, ServiceOperationBatch } from "../../ipc/servicesApi";
import type { TargetSetPickerTarget } from "../../features/execbatch/TargetSetPicker";
import {
  click,
  clickButton,
  flush,
  flushMacrotask,
  flushUntil,
  mountWithClient,
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
  saveForward: vi.fn(),
  ask: vi.fn(),
  openTerminal: vi.fn(),
}));

vi.mock("../../ipc/servicesApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/servicesApi")>();
  return { ...actual, servicesApi: { list: mocks.list, action: mocks.action, find: mocks.find, kill: mocks.kill } };
});
vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return { ...actual, createExecBatchId: mocks.createId, execBatchApi: { ...actual.execBatchApi, cancel: mocks.cancel } };
});
vi.mock("../../ipc/forwardRuleApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/forwardRuleApi")>();
  return { ...actual, forwardRuleApi: { ...actual.forwardRuleApi, save: mocks.saveForward } };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../app/store", () => ({ openTerminalTab: mocks.openTerminal }));

import { ProcessFinderPanel } from "../../features/services/ProcessFinderPanel";

const sessions: SessionInfo[] = [
  { id: "s1", assetId: "a1", name: "web-one", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
  { id: "s2", assetId: "a2", name: "web-two", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
  { id: "s3", assetId: "a3", name: "web-three", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
];
const targets: TargetSetPickerTarget[] = sessions.map((session) => ({
  id: session.id, label: session.name, sessionName: session.name, endpoint: null, tags: ["web"], workspaceNames: [], envCount: 0,
}));

function execResult(sessionId: string, patch: Partial<ExecBatchResult> = {}): ExecBatchResult {
  return {
    sessionId, sessionName: sessions.find((session) => session.id === sessionId)?.name ?? sessionId, status: "success", stdout: "", stderr: "",
    exitCode: 0, durationMs: 1, truncated: false, ...patch,
  };
}

const finderBatch: FinderBatch = {
  batchId: "batch-1",
  command: "probe",
  hosts: [
    {
      result: execResult("s1"), listenerTool: "ss",
      matches: [{
        kind: "listener",
        listener: { address: "0.0.0.0", port: 8080, pid: 1201, program: "nginx" },
        process: { pid: 1201, user: "www-data", cpu: "1.2", memory: "2.3", elapsed: "01:00:00", name: "nginx", args: "nginx: master process" },
      }],
    },
    { result: execResult("s2"), listenerTool: "netstat", listenerError: "缺少 ss 和 netstat，无法查询监听端口", processError: "sudo: a password is required", matches: [] },
  ],
};

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

function matchRow(): HTMLElement {
  const row = panel().container.querySelector<HTMLElement>('tr[data-pid="1201"]');
  if (!row) throw new Error("match row not found");
  return row;
}

function hostSection(id: string): HTMLElement {
  const section = panel().container.querySelector<HTMLElement>(`section[data-session-id="${id}"]`);
  if (!section) throw new Error(`host section ${id} not found`);
  return section;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  nextId = 0;
  mocks.createId.mockImplementation(() => `batch-${++nextId}`);
  mocks.cancel.mockResolvedValue({ canceled: true });
  mocks.find.mockResolvedValue(finderBatch);
  mocks.ask.mockResolvedValue(true);
  mocks.openTerminal.mockResolvedValue(undefined);
  mocks.saveForward.mockImplementation(async (input) => ({
    id: "rule-1", assetId: input.assetId, name: input.name, kind: "local", listenHost: "127.0.0.1",
    listenPort: input.listenPort, targetHost: input.targetHost, targetPort: input.targetPort,
    status: "stopped", createdAt: 1, updatedAt: 1,
  }));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function mountAndFind(selectedIds = ["s1", "s2"]): Promise<void> {
  mounted = mountWithClient(createElement(ProcessFinderPanel, { targets, sessions }));
  for (const id of selectedIds) click(target(id));
  clickButton(panel().container, "查找");
  const lastHost = selectedIds[selectedIds.length - 1];
  await flushUntil(() => panel().container.querySelector(`section[data-session-id="${lastHost}"]`) !== null);
}

// The confirm click starts the async retry; keep an act scope open across a
// macrotask so its state updates land inside act.
async function clickAndFlush(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
}

describe("ProcessFinderPanel", () => {
  it("shows host by match results and per-host tool failures", async () => {
    await mountAndFind();
    expect(mocks.find).toHaveBeenCalledWith(expect.objectContaining({
      sessionIds: ["s1", "s2"], query: { kind: "process", value: "nginx" }, sudo: true,
    }));
    expect(panel().container.textContent).toContain("监听器：netstat");
    expect(panel().container.textContent).toContain("缺少 ss 和 netstat");
    expect(panel().container.textContent).toContain("sudo: a password is required");
    expect(panel().container.textContent).toContain("nginx: master process");
  });

  it("opens a terminal on the match host and saves a P-35 rule to the listener port", async () => {
    await mountAndFind();
    clickButton(matchRow(), "终端");
    await flushUntil(() => panel().container.textContent?.includes("已打开 web-one 的终端") ?? false);
    expect(mocks.openTerminal).toHaveBeenCalledWith(sessions[0]);

    clickButton(matchRow(), "转发");
    const listen = panel().container.querySelector<HTMLInputElement>('input[aria-label="finder 本地监听端口"]');
    if (!listen) throw new Error("listen input not found");
    setInputValue(listen, "18080");
    clickButton(panel().container, "保存规则");
    await flushUntil(() => mocks.saveForward.mock.calls.length === 1);
    expect(mocks.saveForward).toHaveBeenCalledWith({
      assetId: "a1", name: "web-one 127.0.0.1:8080", listenPort: 18080, targetHost: "127.0.0.1", targetPort: 8080,
    });
    await flushUntil(() => panel().container.textContent?.includes("未自动启动") ?? false);
  });

  it("does not execute kill when ordinary confirmation is canceled", async () => {
    mocks.ask.mockResolvedValue(false);
    await mountAndFind();
    clickButton(matchRow(), "kill");
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    expect(mocks.kill).not.toHaveBeenCalled();
    expect(mocks.ask.mock.calls[0][0]).toContain("PID 1201");
  });

  it("requires production confirmation after kill confirmation and keeps sudo failure visible", async () => {
    const targetSetHash = "c".repeat(64);
    const commandHash = "d".repeat(64);
    const failure: ServiceOperationBatch = {
      batchId: "batch-2", command: "kill -TERM -- 1201",
      hosts: [{
        result: execResult("s1", { status: "exit_error", exitCode: 1, stderr: "sudo: a password is required" }),
        systemd: "unknown", failureKind: "sudo", units: [],
      }],
    };
    mocks.kill.mockRejectedValueOnce({
      code: "needs_confirm",
      detail: {
        kind: "exec_batch_production_confirmation", batchId: "batch-2", targetSetHash, commandHash,
        requiredEnvironmentNames: ["prod"],
        productionTargets: [{ sessionId: "s1", sessionName: "web-one", environment: "prod" }],
      },
    }).mockResolvedValueOnce(failure);
    await mountAndFind();
    clickButton(matchRow(), "kill");
    await act(async () => {
      await flush();
    });
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.kill).toHaveBeenCalledTimes(1);
    expect(mocks.kill.mock.calls[0][0]).toMatchObject({ batchId: "batch-2", sessionId: "s1", pid: 1201, sudo: true });
    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
    if (!dialog) throw new Error("production dialog not found");
    expect(dialog.textContent).toContain("kill -TERM -- 1201");
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 web-one"]');
    if (!environmentInput) throw new Error("typed environment input not found");
    setInputValue(environmentInput, "prod");
    clickButton(dialog, "确认执行（1）");
    await act(async () => {
      await flush();
    });
    await flushUntil(() => mocks.kill.mock.calls.length === 2);
    expect(mocks.kill.mock.calls[1][0]).toMatchObject({
      batchId: "batch-2", productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["prod"] },
    });
    await flushUntil(() =>
      panel().container.textContent?.includes("kill 逐目标结果") === true &&
      [...matchRow().querySelectorAll("button")].some((button) => button.textContent?.trim() === "kill" && !button.disabled),
    );
    expect(panel().container.textContent).toContain("sudo 不可用或需要密码");
  });

  it("retries the M15 typed production challenge with the typed environment names", async () => {
    const targetSetHash = "c".repeat(64);
    const commandHash = "d".repeat(64);
    const failure: ServiceOperationBatch = {
      batchId: "batch-2", command: "kill -TERM -- 1201",
      hosts: [{
        result: execResult("s1", { status: "success", exitCode: 0 }),
        systemd: "unknown", failureKind: undefined, units: [],
      }],
    };
    mocks.kill.mockRejectedValueOnce({
      code: "needs_confirm",
      detail: {
        kind: "exec_batch_production_confirmation", batchId: "batch-2", targetSetHash, commandHash,
        requiredEnvironmentNames: ["prod"],
        productionTargets: [{ sessionId: "s1", sessionName: "web-one", environment: "prod" }],
      },
    }).mockResolvedValueOnce(failure);
    await mountAndFind();
    clickButton(matchRow(), "kill");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);

    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]')!;
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 web-one"]');
    if (!environmentInput) throw new Error("typed environment input not found");
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("confirm button not found");
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput, "prod");
    expect(confirm.disabled).toBe(false);
    await clickAndFlush(confirm);
    await flushUntil(() => mocks.kill.mock.calls.length === 2);
    expect(mocks.kill.mock.calls[1][0]).toMatchObject({
      batchId: "batch-2",
      productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["prod"] },
    });
  });

  it("attributes truncation warnings only to the truncated host", async () => {
    mocks.find.mockResolvedValue({
      batchId: "batch-1",
      command: "probe",
      hosts: [
        finderBatch.hosts[0],
        { result: execResult("s2", { truncated: true }), listenerTool: "ss", matches: [] },
      ],
    });
    await mountAndFind();

    expect(hostSection("s1").textContent).not.toContain("输出截断");
    expect(hostSection("s2").textContent).toContain("输出截断：该主机结果不完整");
    expect(hostSection("s2").textContent).toContain("输出截断，无法确认是否还有匹配行");
    expect(hostSection("s2").textContent).not.toContain("该主机没有匹配行");
  });

  it("distinguishes failed, truncated, and confirmed empty hosts", async () => {
    mocks.find.mockResolvedValue({
      batchId: "batch-1",
      command: "probe",
      hosts: [
        { result: execResult("s1", { status: "exit_error", exitCode: 1, error: "transport reset" }), matches: [] },
        { result: execResult("s2", { truncated: true }), listenerTool: "ss", matches: [] },
        { result: execResult("s3"), listenerTool: "ss", matches: [] },
      ],
    });
    await mountAndFind(["s1", "s2", "s3"]);

    expect(hostSection("s1").textContent).toContain("transport reset");
    expect(hostSection("s1").textContent).toContain("探测失败，无法确认是否存在匹配行");
    expect(hostSection("s1").textContent).not.toContain("该主机没有匹配行");
    expect(hostSection("s2").textContent).toContain("输出截断，无法确认是否还有匹配行");
    expect(hostSection("s2").textContent).not.toContain("该主机没有匹配行");
    expect(hostSection("s3").textContent).toContain("该主机没有匹配行");
    expect(hostSection("s3").textContent).not.toContain("探测失败，无法确认是否存在匹配行");
    expect(hostSection("s3").textContent).not.toContain("输出截断");
  });
});
