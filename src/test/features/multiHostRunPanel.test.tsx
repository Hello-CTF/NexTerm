/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, StrictMode } from "react";
import { useUi } from "../../app/store";
import type { Asset, SessionInfo } from "../../ipc/commands";
import type { RunbookRunState, RunbookStartRequest } from "../../ipc/runbookApi";
import {
  click,
  clickButton,
  deferred,
  flushMacrotask,
  flushUntil,
  mountWithClient,
  setInputValue,
  chooseSelectOption,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  start: vi.fn(),
  get: vi.fn(),
  prepare: vi.fn(),
  advance: vi.fn(),
  cancel: vi.fn(),
  historyGet: vi.fn(),
  createId: vi.fn(),
  assetList: vi.fn(),
  terminalWrite: vi.fn(),
}));

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    call: vi.fn(),
    assetApi: { list: mocks.assetList },
    dbApi: {},
    vaultApi: {},
    sessionApi: {},
    terminalApi: { write: mocks.terminalWrite },
  });
});

vi.mock("../../ipc/runbookApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/runbookApi")>();
  return {
    ...actual,
    createRunbookId: mocks.createId,
    runbookApi: {
      start: mocks.start,
      get: mocks.get,
      prepare: mocks.prepare,
      advance: mocks.advance,
      cancel: mocks.cancel,
      historyGet: mocks.historyGet,
    },
  };
});

import { MultiHostRunPanel } from "../../features/explorer/MultiHostRunPanel";
import type { Snippet } from "../../features/explorer/snippetInsert";

function snippetOf(id: string, name: string, steps: Array<{ name?: string; command: string; confirm?: boolean; interactive?: boolean }>): Snippet {
  return {
    id, name, groupId: null, sort: 1,
    body: steps.map((step) => step.command).join("\n"),
    steps: steps.length > 1 || steps.some((step) => step.confirm || step.interactive)
      ? steps.map((step, index) => ({ id: `${id}-step-${index + 1}`, name: step.name ?? "", command: step.command, confirm: step.confirm, interactive: step.interactive }))
      : [],
  };
}

const GREETING_COMMAND = "echo {{asset.name}} {{service=one}}";
const greeting = snippetOf("greeting", "Render greeting", [
  { name: "Render greeting", command: GREETING_COMMAND },
  { name: "Finish", command: "echo {{service=one}} done" },
]);
const oneStepWithVariables = snippetOf("single-vars", "Single greeting", [{ command: GREETING_COMMAND }]);
const trueOnce = snippetOf("true-once", "Finish", [{ command: "true" }]);
const trueTwice = snippetOf("true-twice", "Finish twice", [{ command: "true" }, { command: "true" }]);

let mounted: MountedView | undefined;
let currentState: RunbookRunState | undefined;
let idCounter = 0;

function session(id: string): SessionInfo {
  return { id, assetId: `asset-${id}`, name: `ssh-${id}`, kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
}

function asset(id: string, name: string): Asset {
  return {
    id, groupId: null, kind: "ssh", name, host: `${name}.internal`, port: 22,
    username: null, authKind: "agent", keyPath: null, credId: null, options: {}, tags: "",
    note: "", sort: 0, createdAt: 0, updatedAt: 0, deletedAt: null, builtin: false,
  };
}

function initialState(input: RunbookStartRequest): RunbookRunState {
  return {
    runId: input.runId,
    runbookId: input.runbookId,
    runbookName: input.runbookName,
    status: "awaiting_render",
    confirmationPolicy: input.confirmationPolicy ?? "every_step",
    stopOnFailure: input.stopOnFailure ?? true,
    sessionIds: [...input.sessionIds],
    currentStep: 1,
    stepCount: input.steps.length,
    steps: input.steps.map((step, index) => ({
      number: index + 1,
      step: { ...step },
      status: index === 0 ? "awaiting_render" : "pending",
      results: [],
      batchIds: [],
    })),
  };
}

function finishCurrentStep(stepNumber: number, resultStatus = "success"): RunbookRunState {
  if (!currentState) throw new Error("run not started");
  const next = structuredClone(currentState);
  const step = next.steps[stepNumber - 1];
  step.status = resultStatus === "success" ? "success" : "failed";
  step.batchIds = [`batch-${stepNumber}`];
  step.results = (step.renderedCommands ?? []).map((rendered) => ({
    sessionId: rendered.sessionId,
    sessionName: `ssh-${rendered.sessionId}`,
    status: resultStatus,
    stdout: rendered.command,
    stderr: "",
    exitCode: resultStatus === "success" ? 0 : 17,
    durationMs: 1,
    truncated: false,
  }));
  if (resultStatus !== "success" && next.stopOnFailure) {
    next.status = "failed";
    for (const later of next.steps.slice(stepNumber)) later.status = "skipped";
  } else if (stepNumber === next.stepCount) {
    next.status = resultStatus === "success" ? "success" : "failed";
  } else {
    next.currentStep = stepNumber + 1;
    next.status = "awaiting_render";
    next.steps[next.currentStep - 1].status = "awaiting_render";
  }
  currentState = next;
  return next;
}

function canceledState(): RunbookRunState {
  if (!currentState) throw new Error("run not started");
  const next = structuredClone(currentState);
  next.status = "canceled";
  const current = next.steps[next.currentStep - 1];
  current.status = "canceled";
  current.results = (current.renderedCommands ?? []).map((rendered) => ({
    sessionId: rendered.sessionId,
    sessionName: `ssh-${rendered.sessionId}`,
    status: "canceled",
    stdout: "",
    stderr: "",
    exitCode: null,
    durationMs: 0,
    truncated: false,
  }));
  for (const later of next.steps.slice(next.currentStep)) later.status = "skipped";
  currentState = next;
  return next;
}

function panel(): MountedView {
  if (!mounted) throw new Error("panel not mounted");
  return mounted;
}

function text(): string {
  return panel().container.textContent ?? "";
}

function targetCheckbox(id: string): HTMLInputElement {
  const input = panel().container.querySelector<HTMLInputElement>(`input[type="checkbox"][value="${id}"]`);
  if (!input) throw new Error(`target ${id} not found`);
  return input;
}

function openPanel(snippet: Snippet, strict = false): void {
  mounted?.unmount();
  const panelNode = createElement(MultiHostRunPanel, { snippet, onClose: vi.fn() });
  mounted = mountWithClient(strict ? createElement(StrictMode, null, panelNode) : panelNode);
}

const PRODUCTION_ONLY_LABEL = "只确认标记了『执行前确认』的步骤和生产主机";

// Assets feed the built-in variables; wait until the list query has landed.
async function settleAssets(): Promise<void> {
  await act(async () => {
    await flushMacrotask();
  });
}

async function fillVariablesAndContinue(value?: string): Promise<void> {
  await flushUntil(() => document.body.textContent?.includes("填写变量") ?? false);
  const prompt = [...document.body.querySelectorAll<HTMLElement>('[role="dialog"]')].find(
    (dialog) => dialog.textContent?.includes("填写变量"),
  );
  if (!prompt) throw new Error("variable prompt not found");
  if (value !== undefined) setInputValue(prompt.querySelector<HTMLInputElement>("input")!, value);
  clickButton(prompt, "继续");
}

function startRun(): void {
  click(targetCheckbox("s1"));
  click(targetCheckbox("s2"));
  clickButton(panel().container, "开始运行");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  document.body.style.overflow = "";
  currentState = undefined;
  idCounter = 0;
  mocks.createId.mockImplementation(() => `id-${++idCounter}`);
  mocks.assetList.mockResolvedValue([asset("asset-s1", "api-a"), asset("asset-s2", "api-b")]);
  mocks.terminalWrite.mockResolvedValue(undefined);
  mocks.start.mockImplementation((input: RunbookStartRequest) => {
    currentState = initialState(input);
    return Promise.resolve(currentState);
  });
  mocks.prepare.mockImplementation((input: { stepNumber: number; commands: { sessionId: string; command: string }[] }) => {
    if (!currentState) throw new Error("run not started");
    const next = structuredClone(currentState);
    next.status = "awaiting_confirmation";
    next.currentStep = input.stepNumber;
    const step = next.steps[input.stepNumber - 1];
    step.status = "awaiting_confirmation";
    step.renderedCommands = input.commands;
    currentState = next;
    return Promise.resolve(next);
  });
  mocks.advance.mockImplementation((input: { stepNumber: number }) => Promise.resolve(finishCurrentStep(input.stepNumber)));
  mocks.cancel.mockImplementation(() => Promise.resolve(canceledState()));
  mocks.historyGet.mockResolvedValue({
    runbookId: "runbook-history",
    runId: "run-history",
    status: "failed",
    reachedStep: 1,
    stepCount: 2,
    batches: [{
      batch: {
        id: "batch-history", commandHash: "a".repeat(64), targetSetHash: "b".repeat(64),
        status: "failed", targetCount: 1, production: false, startedAt: 1, finishedAt: 2,
        runbookId: "runbook-history", runId: "run-history", stepNumber: 1, stepCount: 2, syncOptIn: false,
      },
      results: [{
        sessionId: "s1", position: 0, sessionName: "ssh-s1", assetId: "asset-s1", environment: "production",
        status: "exit_error", exitCode: 17, signal: "", errorKind: "remote_exit",
        startedAt: 1, finishedAt: 2, durationMs: 1, truncated: false,
      }],
    }],
  });
  useUi.setState({ sessions: [session("s1"), session("s2")], workspaces: [], toasts: [], pushToast: vi.fn() });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
  document.body.style.overflow = "";
});

describe("MultiHostRunPanel", () => {
  it("starts the run from the snippet with its id, name, and per-step flags", async () => {
    openPanel(snippetOf("deploy", "Deploy app", [
      { name: "build", command: "make", confirm: true },
      { name: "probe", command: "curl -fsS localhost", interactive: true },
      { command: "true" },
    ]));
    await settleAssets();
    startRun();
    await flushUntil(() => text().includes("请确认下列命令"));
    const request = mocks.start.mock.calls[0][0] as RunbookStartRequest;
    expect(request.runbookId).toBe("deploy");
    expect(request.runbookName).toBe("Deploy app");
    expect(request.steps.map((step) => ({ command: step.command, confirm: step.confirm, interactive: step.interactive }))).toEqual([
      { command: "make", confirm: true, interactive: undefined },
      { command: "curl -fsS localhost", interactive: true, confirm: undefined },
      { command: "true", confirm: undefined, interactive: undefined },
    ]);
    expect(request.steps[0].name).toBe("build");
    expect(request.steps[2].name).toBeTruthy();
  });

  it("shows the snippet steps read-only and offers no step editor", () => {
    openPanel(snippetOf("deploy", "Deploy app", [
      { name: "build", command: "make", confirm: true },
      { name: "probe", command: "curl -fsS localhost", interactive: true },
    ]));
    expect(text()).toContain("Deploy app");
    expect(text()).toContain("make");
    expect(text()).toContain("curl -fsS localhost");
    expect(panel().container.querySelectorAll("textarea")).toHaveLength(0);
    expect(panel().container.querySelector("#runbook-name")).toBeNull();
    expect(panel().container.querySelector('[data-testid="multi-host-step-1"]')?.textContent).toContain("执行前确认");
    expect(panel().container.querySelector('[data-testid="multi-host-step-2"]')?.textContent).toContain("交互指令");
    expect(panel().container.querySelector('[data-testid="multi-host-step-2"]')?.textContent).not.toContain("执行前确认");
  });

  it("treats a single-command snippet as one step", async () => {
    openPanel(trueOnce);
    await settleAssets();
    startRun();
    await flushUntil(() => text().includes("请确认下列命令"));
    const request = mocks.start.mock.calls[0][0] as RunbookStartRequest;
    expect(request.steps).toHaveLength(1);
    expect(request.steps[0].command).toBe("true");
    expect(request.runbookId).toBe("true-once");
  });

  it("confirms the exact rendered commands at every step and keeps prior results visible", async () => {
    openPanel(greeting);
    await settleAssets();
    startRun();
    await fillVariablesAndContinue();
    await flushUntil(() => text().includes("请确认下列命令"));
    expect(mocks.start).toHaveBeenCalledTimes(1);

    expect(mocks.prepare).toHaveBeenCalledWith(expect.objectContaining({
      stepNumber: 1,
      commands: [
        { sessionId: "s1", command: "echo api-a one" },
        { sessionId: "s2", command: "echo api-b one" },
      ],
    }));
    expect(text()).toContain("echo api-a one");
    expect(text()).toContain("echo api-b one");
    expect(text()).not.toContain("{{asset.name}}");
    expect(mocks.advance).not.toHaveBeenCalled();

    clickButton(panel().container, "确认并执行第 1 步");
    await flushUntil(() => text().includes("确认并执行第 2 步"));
    // The shared values were asked for once; the second step renders without another form.
    expect(document.body.textContent).not.toContain("填写变量");
    expect(mocks.advance).toHaveBeenCalledTimes(1);
    expect(panel().container.querySelector('[data-testid="runbook-step-results-1"]')?.textContent).toContain("echo api-a one");
    expect(mocks.prepare).toHaveBeenLastCalledWith(expect.objectContaining({
      stepNumber: 2,
      commands: [
        { sessionId: "s1", command: "echo one done" },
        { sessionId: "s2", command: "echo one done" },
      ],
    }));

    clickButton(panel().container, "确认并执行第 2 步");
    await flushUntil(() => text().includes("查看本次运行历史"));
    expect(mocks.advance.mock.calls.map(([input]) => input.stepNumber)).toEqual([1, 2]);
    expect(mocks.start.mock.calls[0][0]).toMatchObject({ confirmationPolicy: "every_step", stopOnFailure: true });
  });

  it("uses production-only mode without bypassing the backend production challenge", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.advance.mockImplementationOnce((input: { stepNumber: number }) => {
      if (!currentState) throw new Error("run not started");
      const next = structuredClone(currentState);
      next.status = "awaiting_production_confirmation";
      next.steps[input.stepNumber - 1].status = "awaiting_production_confirmation";
      next.productionCommand = "echo api-a one";
      next.productionChallenge = {
        kind: "exec_batch_production_confirmation", batchId: "batch-prod", targetSetHash, commandHash,
        requiredEnvironmentNames: ["production"],
        productionTargets: [{ sessionId: "s1", sessionName: "ssh-s1", environment: "production" }],
      };
      currentState = next;
      return Promise.resolve(next);
    }).mockImplementationOnce((input: { stepNumber: number }) => Promise.resolve(finishCurrentStep(input.stepNumber)));
    openPanel(oneStepWithVariables);
    chooseSelectOption(panel().container, "执行前确认", PRODUCTION_ONLY_LABEL);
    await settleAssets();
    click(targetCheckbox("s1"));
    clickButton(panel().container, "开始运行");
    await fillVariablesAndContinue();
    await flushUntil(() => document.body.querySelector('[aria-label="确认生产环境执行"]') !== null);

    expect(mocks.advance).toHaveBeenCalledWith(expect.objectContaining({ stepNumber: 1, confirmed: false }));
    expect(text()).not.toContain("确认并执行第 1 步");
    const dialog = document.body.querySelector<HTMLElement>('[aria-label="确认生产环境执行"]')!;
    expect(dialog.textContent).toContain("echo api-a one");
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 ssh-s1"]');
    if (!environmentInput) throw new Error("typed environment input not found");
    setInputValue(environmentInput, "production");
    clickButton(dialog, "确认执行（1）");
    await flushUntil(() => text().includes("查看本次运行历史"));
    expect(mocks.advance).toHaveBeenCalledTimes(2);
    expect(mocks.advance.mock.calls[1][0]).toMatchObject({
      stepNumber: 1,
      productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["production"] },
    });
    expect(mocks.start.mock.calls[0][0]).toMatchObject({ confirmationPolicy: "production_only" });
  });

  it("retries the M15 typed production challenge with the typed environment names", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.advance.mockImplementationOnce((input: { stepNumber: number }) => {
      if (!currentState) throw new Error("run not started");
      const next = structuredClone(currentState);
      next.status = "awaiting_production_confirmation";
      next.steps[input.stepNumber - 1].status = "awaiting_production_confirmation";
      next.productionCommand = "echo api-a one";
      next.productionChallenge = {
        kind: "exec_batch_production_confirmation", batchId: "batch-prod", targetSetHash, commandHash,
        requiredEnvironmentNames: ["production"],
        productionTargets: [{ sessionId: "s1", sessionName: "ssh-s1", environment: "production" }],
      };
      currentState = next;
      return Promise.resolve(next);
    }).mockImplementationOnce((input: { stepNumber: number }) => Promise.resolve(finishCurrentStep(input.stepNumber)));
    openPanel(oneStepWithVariables);
    chooseSelectOption(panel().container, "执行前确认", PRODUCTION_ONLY_LABEL);
    await settleAssets();
    click(targetCheckbox("s1"));
    clickButton(panel().container, "开始运行");
    await fillVariablesAndContinue();
    await flushUntil(() => document.body.querySelector('[aria-label="确认生产环境执行"]') !== null);

    const dialog = document.body.querySelector<HTMLElement>('[aria-label="确认生产环境执行"]')!;
    expect(dialog.textContent).toContain("echo api-a one");
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 ssh-s1"]');
    if (!environmentInput) throw new Error("typed environment input not found");
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("production confirm button not found");
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput, "production");
    expect(confirm.disabled).toBe(false);
    clickButton(dialog, "确认执行（1）");
    await flushUntil(() => text().includes("查看本次运行历史"));
    expect(mocks.advance).toHaveBeenCalledTimes(2);
    expect(mocks.advance.mock.calls[1][0]).toMatchObject({
      stepNumber: 1,
      productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["production"] },
    });
  });

  it("stops after a failed step and reads back the run history", async () => {
    mocks.advance.mockImplementation((input: { stepNumber: number }) => Promise.resolve(finishCurrentStep(input.stepNumber, "exit_error")));
    openPanel(snippetOf("fails", "Fails", [{ command: "true" }, { command: "echo second" }]));
    await settleAssets();
    startRun();
    await flushUntil(() => text().includes("确认并执行第 1 步"));
    clickButton(panel().container, "确认并执行第 1 步");
    await flushUntil(() => text().includes("查看本次运行历史"));

    expect(mocks.prepare).toHaveBeenCalledTimes(1);
    expect(mocks.advance).toHaveBeenCalledTimes(1);
    expect(text()).toContain("已跳过");
    expect(panel().container.querySelector('[data-testid="runbook-step-results-1"]')?.textContent).toContain("命令失败");
    clickButton(panel().container, "查看本次运行历史");
    await flushUntil(() => panel().container.querySelector('[data-testid="runbook-history"]') !== null);
    const history = panel().container.querySelector('[data-testid="runbook-history"]')!;
    expect(history.querySelector('[title^="片段 runbook-history"]')).not.toBeNull();
    expect(history.textContent).toContain("到第 1 / 2 步");
    expect(history.textContent).toContain("ssh-s1");
    expect(history.textContent).toContain("remote_exit");
    expect(mocks.historyGet).toHaveBeenCalledWith(mocks.start.mock.calls[0][0].runId);
  });

  it("cancels only the active run while advance is pending and never writes to a terminal", async () => {
    const pending = deferred<RunbookRunState>();
    const pendingCancel = deferred<RunbookRunState>();
    mocks.advance.mockReturnValue(pending.promise);
    mocks.cancel.mockReturnValue(pendingCancel.promise);
    openPanel(trueOnce);
    await settleAssets();
    startRun();
    await flushUntil(() => text().includes("确认并执行第 1 步"));
    clickButton(panel().container, "确认并执行第 1 步");
    await flushUntil(() => mocks.advance.mock.calls.length === 1);
    clickButton(panel().container, "取消执行");
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);
    expect(mocks.cancel).toHaveBeenCalledWith(mocks.start.mock.calls[0][0].runId);
    await act(async () => {
      pendingCancel.resolve(canceledState());
      await flushMacrotask();
    });
    await act(async () => {
      pending.resolve(canceledState());
      await flushMacrotask();
    });
    await flushUntil(() => text().includes("已取消"));
    expect(mocks.advance).toHaveBeenCalledTimes(1);
    expect(mocks.prepare).toHaveBeenCalledTimes(1);
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
  });

  it("does not revive a canceled run when an older prepare response arrives late", async () => {
    const pendingPrepare = deferred<RunbookRunState>();
    mocks.prepare.mockReturnValue(pendingPrepare.promise);
    openPanel(trueOnce);
    await settleAssets();
    startRun();
    await flushUntil(() => mocks.prepare.mock.calls.length === 1);
    clickButton(panel().container, "取消执行");
    await flushUntil(() => text().includes("已取消"));
    if (!currentState) throw new Error("run not started");
    const stale = structuredClone(currentState);
    stale.status = "awaiting_confirmation";
    stale.steps[0].status = "awaiting_confirmation";
    stale.steps[0].renderedCommands = [{ sessionId: "s1", command: "true" }, { sessionId: "s2", command: "true" }];
    await act(async () => {
      pendingPrepare.resolve(stale);
      await flushMacrotask();
    });
    expect(text()).toContain("已取消");
    expect(text()).not.toContain("确认并执行第 1 步");
    expect(mocks.advance).not.toHaveBeenCalled();
  });

  it("cancels a late start after close without preparing or advancing", async () => {
    const pendingStart = deferred<RunbookRunState>();
    mocks.start.mockImplementation((input: RunbookStartRequest) => {
      currentState = initialState(input);
      return pendingStart.promise;
    });
    mocks.cancel.mockResolvedValue(undefined);
    openPanel(trueOnce);
    chooseSelectOption(panel().container, "执行前确认", PRODUCTION_ONLY_LABEL);
    await settleAssets();
    startRun();
    await flushUntil(() => mocks.start.mock.calls.length === 1);
    const runId = mocks.start.mock.calls[0][0].runId as string;
    clickButton(panel().container, "关闭");
    panel().unmount();
    mounted = undefined;
    await act(async () => {
      if (!currentState) throw new Error("start input missing");
      pendingStart.resolve(currentState);
      await flushMacrotask();
    });

    expect(mocks.prepare).not.toHaveBeenCalled();
    expect(mocks.advance).not.toHaveBeenCalled();
    expect(mocks.cancel.mock.calls.length).toBeGreaterThan(0);
    for (const [canceledId] of mocks.cancel.mock.calls) expect(canceledId).toBe(runId);
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
  });

  it("does not advance when a prepare response arrives after close", async () => {
    const pendingPrepare = deferred<RunbookRunState>();
    mocks.prepare.mockReturnValue(pendingPrepare.promise);
    openPanel(trueOnce);
    chooseSelectOption(panel().container, "执行前确认", PRODUCTION_ONLY_LABEL);
    await settleAssets();
    startRun();
    await flushUntil(() => mocks.prepare.mock.calls.length === 1);
    if (!currentState) throw new Error("run not started");
    const prepared = structuredClone(currentState);
    prepared.status = "awaiting_confirmation";
    prepared.steps[0].status = "awaiting_confirmation";
    prepared.steps[0].renderedCommands = [
      { sessionId: "s1", command: "true" },
      { sessionId: "s2", command: "true" },
    ];
    clickButton(panel().container, "关闭");
    panel().unmount();
    mounted = undefined;
    await act(async () => {
      pendingPrepare.resolve(prepared);
      await flushMacrotask();
    });

    expect(mocks.advance).not.toHaveBeenCalled();
    expect(mocks.cancel).toHaveBeenCalledWith(prepared.runId);
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
  });

  it("keeps UI, drive, and busy state aligned under StrictMode", async () => {
    const pendingCancel = deferred<RunbookRunState>();
    mocks.cancel.mockReturnValue(pendingCancel.promise);
    openPanel(trueTwice, true);
    await settleAssets();
    startRun();
    await flushUntil(() => text().includes("确认并执行第 1 步"));
    const firstConfirm = [...panel().container.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "确认并执行第 1 步",
    );
    if (!firstConfirm) throw new Error("first step confirmation not found");
    expect(firstConfirm.disabled).toBe(false);
    click(firstConfirm);

    await flushUntil(() => text().includes("确认并执行第 2 步"));
    const secondConfirm = panel().container.querySelector<HTMLButtonElement>("button.nx-btn-primary");
    if (!secondConfirm) throw new Error("second step confirmation not found");
    expect(secondConfirm.disabled).toBe(false);
    expect(mocks.advance).toHaveBeenCalledTimes(1);
    expect(mocks.prepare).toHaveBeenCalledTimes(2);

    clickButton(panel().container, "取消执行");
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);
    await act(async () => {
      pendingCancel.resolve(canceledState());
      await flushMacrotask();
    });
    await flushUntil(() => text().includes("已取消"));
    expect(mocks.advance).toHaveBeenCalledTimes(1);
    panel().unmount();
    mounted = undefined;
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
  });

  it("does not auto-advance a step marked for confirmation under the production-only policy", async () => {
    openPanel(snippetOf("guarded", "Guarded", [
      { name: "safe", command: "true" },
      { name: "restart", command: "systemctl restart app", confirm: true },
      { name: "after", command: "echo after" },
    ]));
    chooseSelectOption(panel().container, "执行前确认", PRODUCTION_ONLY_LABEL);
    await settleAssets();
    startRun();
    // Step 1 is not marked, so it advances unconfirmed; step 2 stops for the user.
    await flushUntil(() => text().includes("确认并执行第 2 步"));
    expect(mocks.advance.mock.calls.map(([input]) => [input.stepNumber, input.confirmed])).toEqual([[1, false]]);

    clickButton(panel().container, "确认并执行第 2 步");
    await flushUntil(() => text().includes("查看本次运行历史"));
    expect(mocks.advance.mock.calls.map(([input]) => [input.stepNumber, input.confirmed])).toEqual([
      [1, false], [2, true], [3, false],
    ]);
  });

  it("stops before the run when the variable form is canceled", async () => {
    openPanel(oneStepWithVariables);
    await settleAssets();
    startRun();
    await flushUntil(() => document.body.textContent?.includes("填写变量") ?? false);
    const prompt = [...document.body.querySelectorAll<HTMLElement>('[role="dialog"]')].find(
      (dialog) => dialog.textContent?.includes("填写变量"),
    );
    clickButton(prompt!, "取消");
    await flushUntil(() => text().includes("已取消填写变量"));
    expect(mocks.start).not.toHaveBeenCalled();
  });

  it("rejects steps whose shared variables conflict", async () => {
    openPanel(snippetOf("conflict", "Conflict", [{ command: "a {{svc=one}}" }, { command: "b {{svc=two}}" }]));
    await settleAssets();
    startRun();
    await flushUntil(() => text().includes("不一致"));
    expect(mocks.start).not.toHaveBeenCalled();
  });
});
