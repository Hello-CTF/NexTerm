/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { useAuth } from "../../features/auth/store";
import type { AccountUser } from "../../ipc/authApi";
import type { SessionInfo } from "../../ipc/commands";
import { ExecBatchPanel } from "../../features/execbatch/ExecBatchPanel";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
  mountWithClient,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn<(command: string, args?: unknown) => Promise<unknown>>(),
  execute: vi.fn(),
  cancel: vi.fn(),
  createId: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    ...actual,
    call: mocks.call,
    assetApi: { list: vi.fn().mockResolvedValue([]) },
    dbApi: {},
    vaultApi: {},
    sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn().mockResolvedValue([]) },
    terminalApi: {
      listLive: vi.fn().mockResolvedValue([]),
      listDurable: vi.fn().mockResolvedValue([]),
      write: vi.fn().mockResolvedValue(undefined),
      detach: vi.fn().mockResolvedValue(undefined),
      setVisible: vi.fn().mockResolvedValue(undefined),
      closeTab: vi.fn().mockResolvedValue(undefined),
    },
  });
});

vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return {
    ...actual,
    execBatchApi: {
      execute: mocks.execute,
      cancel: mocks.cancel,
      historyList: vi.fn().mockResolvedValue({ items: [], total: 0, limit: 20, offset: 0 }),
      historyGet: vi.fn().mockRejectedValue(new Error("no history")),
    },
    createExecBatchId: mocks.createId,
  };
});

function account(id: string): AccountUser {
  return {
    id,
    username: id,
    display_name: "",
    role: "superadmin",
    state: "active",
    must_change_password: false,
    mfa_enabled: false,
    created_at: 1,
    updated_at: 1,
    last_login_at: 1,
  } as AccountUser;
}

function session(id: string, name: string): SessionInfo {
  return { id, assetId: `asset-${id}`, name, kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
}

const progressSnapshot = (batchId: string, failed: number, phase = "finished") => ({
  batch: {
    batchId,
    phase,
    total: 2,
    pending: 0,
    running: phase === "running" ? 1 : 0,
    succeeded: 2 - failed,
    failed,
    canceled: 0,
    finishedAt: phase === "finished" ? 2 : undefined,
  },
  targets: [
    {
      sessionId: "s1",
      position: 0,
      phase: "finished",
      status: "success",
      output: {
        stdout: { head: "ok", tail: "", totalBytes: 2, droppedBytes: 0, truncated: false },
        stderr: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
      },
      startedAt: 1,
      finishedAt: 2,
      durationMs: 1,
    },
    {
      sessionId: "s2",
      position: 1,
      phase: "finished",
      status: failed > 0 ? "exit_error" : "success",
      exitCode: failed > 0 ? 2 : 0,
      output: {
        stdout: { head: "partial", tail: "", totalBytes: 7, droppedBytes: 0, truncated: false },
        stderr: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
      },
      startedAt: 1,
      finishedAt: 2,
      durationMs: 1,
    },
  ],
});

let mounted: MountedView | null = null;

beforeEach(() => {
  useAuth.setState({ user: account("owner-a") });
  mocks.call.mockReset();
  mocks.execute.mockReset();
  mocks.cancel.mockReset();
  mocks.createId.mockReset();
  mocks.createId.mockReturnValue("batch-1");
  mocks.cancel.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  useAuth.setState({ user: null });
});

function mountPanel(options: { progressPollIntervalMs?: number } = {}) {
  mounted = mountWithClient(
    createElement(ExecBatchPanel, {
      sessions: [session("s1", "alpha"), session("s2", "beta")],
      progressPollIntervalMs: options.progressPollIntervalMs,
    }),
  );
  return mounted;
}

async function runExecuteWithResults(results: { sessionId: string; status: string }[]) {
  mocks.execute.mockResolvedValue({
    batchId: "batch-1",
    command: "uname -a",
    startedAt: 1,
    finishedAt: 2,
    results,
  });
  const view = mountPanel();
  const commandInput = view.container.querySelector<HTMLInputElement>("input[aria-label='批量执行命令']");
  if (!commandInput) throw new Error("command input missing");
  setInputValue(commandInput, "uname -a");
  const checkbox = view.container.querySelector<HTMLInputElement>("input[aria-label='选择目标 alpha']");
  if (!checkbox) throw new Error("target checkbox missing");
  click(checkbox);
  clickButton(view.container, "执行");
  await flushUntil(() => view.container.textContent?.includes("结果矩阵") ?? false);
  return view;
}

describe("ExecBatchPanel target-set wiring", () => {
  it("renders saved sets and applies one to the picker", async () => {
    mocks.call.mockImplementation((command: string, _args?: unknown) => {
      if (command === "target_set_list") {
        return Promise.resolve([{ id: "set-1", name: "web", sessionIds: ["s1"], createdAt: 1, updatedAt: 1 }]);
      }
      return Promise.resolve(undefined);
    });
    const view = mountPanel();
    await flushUntil(() => view.container.textContent?.includes("web") ?? false);

    const apply = view.container.querySelector("button[aria-label='选择目标集 web']");
    if (!apply) throw new Error("apply button missing");
    click(apply);
    const checkbox = view.container.querySelector<HTMLInputElement>("input[aria-label='选择目标 alpha']");
    expect(checkbox?.checked).toBe(true);
    const other = view.container.querySelector<HTMLInputElement>("input[aria-label='选择目标 beta']");
    expect(other?.checked).toBe(false);
  });

  it("saves the current picker selection as a new set", async () => {
    mocks.call.mockResolvedValue([]);
    const view = mountPanel();
    const checkbox = view.container.querySelector<HTMLInputElement>("input[aria-label='选择目标 beta']");
    if (!checkbox) throw new Error("target checkbox missing");
    click(checkbox);
    const nameInput = view.container.querySelector<HTMLInputElement>("input[aria-label='目标集名称']");
    if (!nameInput) throw new Error("name input missing");
    setInputValue(nameInput, "  cache  ");
    clickButton(view.container, "保存当前选择");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("target_set_create", { name: "cache", sessionIds: ["s2"] });
  });

  it("renames and deletes a saved set", async () => {
    mocks.call.mockImplementation((command: string, _args?: unknown) => {
      if (command === "target_set_list") {
        return Promise.resolve([{ id: "set-1", name: "web", sessionIds: ["s1"], createdAt: 1, updatedAt: 1 }]);
      }
      return Promise.resolve(undefined);
    });
    const view = mountPanel();
    await flushUntil(() => view.container.textContent?.includes("web") ?? false);

    clickButton(view.container, "重命名");
    const renameInput = view.container.querySelector<HTMLInputElement>("input[aria-label='重命名目标集 web']");
    if (!renameInput) throw new Error("rename input missing");
    setInputValue(renameInput, "web primary");
    clickButton(view.container, "保存名称");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("target_set_update", { id: "set-1", name: "web primary", sessionIds: ["s1"] });

    clickButton(view.container, "删除");
    clickButton(view.container, "确认删除");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("target_set_delete", { id: "set-1" });
  });
});

describe("ExecBatchPanel progress and rerun wiring", () => {
  it("shows progress for the executed batch and reruns failed targets", async () => {
    mocks.createId.mockReturnValueOnce("batch-1").mockReturnValue("batch-2");
    mocks.call.mockImplementation((command: string, args?: unknown) => {
      const batchId = (args as { batchId?: string } | undefined)?.batchId;
      if (command === "exec_batch_progress") {
        return Promise.resolve(progressSnapshot(batchId ?? "batch-1", 1));
      }
      if (command === "exec_batch_rerun") {
        return Promise.resolve({ batchId: "batch-2", command: "uname -a", startedAt: 3, finishedAt: 4, results: [] });
      }
      if (command === "target_set_list") {
        return Promise.resolve([]);
      }
      return Promise.resolve(undefined);
    });
    const view = await runExecuteWithResults([
      { sessionId: "s1", status: "success" },
      { sessionId: "s2", status: "exit_error" },
    ]);
    await flushUntil(() => view.container.textContent?.includes("重新执行失败目标") ?? false);
    clickButton(view.container, "重新执行失败目标");
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("exec_batch_rerun", {
      batchId: "batch-2",
      command: "uname -a",
      timeoutMs: 30000,
      maxConcurrent: 4,
      results: [
        { sessionId: "s1", status: "success" },
        { sessionId: "s2", status: "exit_error" },
      ],
    });
    await flushUntil(() =>
      mocks.call.mock.calls.some(
        (entry) => entry[0] === "exec_batch_progress" && (entry[1] as { batchId?: string } | undefined)?.batchId === "batch-2",
      ),
    );
  });

  it("routes a rerun production challenge through the confirmation dialog", async () => {
    const detail = {
      kind: "exec_batch_production_confirmation",
      batchId: "batch-2",
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [
        { sessionId: "s2", sessionName: "beta", environment: "production", hostname: "api.production.example" },
      ],
    };
    mocks.createId.mockReturnValueOnce("batch-1").mockReturnValue("batch-2");
    mocks.call.mockImplementation((command: string, _args?: unknown) => {
      if (command === "exec_batch_progress") {
        return Promise.resolve(progressSnapshot("batch-1", 1));
      }
      if (command === "exec_batch_rerun") {
        return Promise.reject({ code: "needs_confirm", message: "生产环境批量执行需要显式确认", detail });
      }
      if (command === "target_set_list") {
        return Promise.resolve([]);
      }
      return Promise.resolve(undefined);
    });
    const view = await runExecuteWithResults([
      { sessionId: "s1", status: "success" },
      { sessionId: "s2", status: "exit_error" },
    ]);
    await flushUntil(() => view.container.textContent?.includes("重新执行失败目标") ?? false);
    clickButton(view.container, "重新执行失败目标");
    await flushUntil(() => document.body.querySelector("[role='dialog']") !== null);

    const envInput = document.body.querySelector<HTMLInputElement>("input[aria-label='输入环境名 api.production.example']");
    if (!envInput) throw new Error("environment input missing");
    setInputValue(envInput, "production");
    const confirm = [...document.body.querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("confirm button missing");
    click(confirm);
    await flush();
    expect(mocks.call).toHaveBeenCalledWith("exec_batch_rerun", {
      batchId: "batch-2",
      command: "uname -a",
      timeoutMs: 30000,
      maxConcurrent: 4,
      results: [
        { sessionId: "s1", status: "success" },
        { sessionId: "s2", status: "exit_error" },
      ],
      productionConfirmation: {
        confirmed: true,
        targetSetHash: "a".repeat(64),
        commandHash: "b".repeat(64),
        environmentNames: ["production"],
      },
    });
  });

  it("keeps progress polling through the ordinary execute startup window", async () => {
    const executeCall = deferred<unknown>();
    mocks.execute.mockReturnValue(executeCall.promise);
    let progressCalls = 0;
    mocks.call.mockImplementation((command: string, _args?: unknown) => {
      if (command === "target_set_list") return Promise.resolve([]);
      if (command === "exec_batch_progress") {
        progressCalls += 1;
        if (progressCalls === 1) {
          return Promise.reject({ code: "not_found", message: "批量执行不存在或已结束" });
        }
        return Promise.resolve(progressSnapshot("batch-1", 0, "running"));
      }
      return Promise.resolve(undefined);
    });
    const view = mountPanel({ progressPollIntervalMs: 5 });
    const commandInput = view.container.querySelector<HTMLInputElement>("input[aria-label='批量执行命令']");
    if (!commandInput) throw new Error("command input missing");
    setInputValue(commandInput, "uname -a");
    const checkbox = view.container.querySelector<HTMLInputElement>("input[aria-label='选择目标 alpha']");
    if (!checkbox) throw new Error("target checkbox missing");
    click(checkbox);
    clickButton(view.container, "执行");

    // The first progress poll sees the pre-tracker not_found; the startup
    // grace keeps polling and renders the running snapshot.
    await flushUntil(() => progressCalls >= 2);
    await flushUntil(() => view.container.textContent?.includes("取消执行") ?? false);
    await act(async () => {
      executeCall.resolve(undefined);
    });
  });

  it("routes the confirmed retry through the panel continuation with live progress and final output", async () => {
    const detail = {
      kind: "exec_batch_production_confirmation",
      batchId: "batch-2",
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [
        { sessionId: "s2", sessionName: "beta", environment: "production", hostname: "api.production.example" },
      ],
    };
    const rerunCall = deferred<unknown>();
    mocks.createId.mockReturnValueOnce("batch-1").mockReturnValue("batch-2");
    mocks.call.mockImplementation((command: string, args?: unknown) => {
      if (command === "target_set_list") return Promise.resolve([]);
      if (command === "exec_batch_progress") {
        const batchId = (args as { batchId?: string } | undefined)?.batchId;
        if (batchId === "batch-2") return Promise.resolve(progressSnapshot("batch-2", 0, "running"));
        return Promise.resolve(progressSnapshot("batch-1", 1));
      }
      if (command === "exec_batch_rerun") {
        if (!(args as { productionConfirmation?: unknown } | undefined)?.productionConfirmation) {
          return Promise.reject({ code: "needs_confirm", message: "生产环境批量执行需要显式确认", detail });
        }
        return rerunCall.promise;
      }
      return Promise.resolve(undefined);
    });
    const view = await runExecuteWithResults([
      { sessionId: "s1", status: "success" },
      { sessionId: "s2", status: "exit_error" },
    ]);
    await flushUntil(() => view.container.textContent?.includes("重新执行失败目标") ?? false);
    clickButton(view.container, "重新执行失败目标");
    await flushUntil(() => document.body.querySelector("[role='dialog']") !== null);

    const envInput = document.body.querySelector<HTMLInputElement>("input[aria-label='输入环境名 api.production.example']");
    if (!envInput) throw new Error("environment input missing");
    setInputValue(envInput, "production");
    const confirm = [...document.body.querySelectorAll("button")].find(
      (button) => button.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("confirm button missing");
    click(confirm);

    // The confirmed retry runs through the panel continuation: the new
    // batch's live progress renders while the retry is pending.
    await flushUntil(() => view.container.textContent?.includes("取消执行") ?? false);
    await act(async () => {
      rerunCall.resolve({
        batchId: "batch-2",
        command: "uname -a",
        startedAt: 3,
        finishedAt: 4,
        results: [{ sessionId: "s2", status: "success", stdout: "confirmed-fixed" }],
      });
    });
    await flushUntil(() => view.container.textContent?.includes("confirmed-fixed") ?? false);
    expect(view.container.textContent).toContain("批次 batch-2 已结束");
  });

  it("drops a stale saved-set response after an account switch", async () => {
    const ownerAList = deferred<unknown>();
    let listCalls = 0;
    mocks.call.mockImplementation((command: string, _args?: unknown) => {
      if (command === "target_set_list") {
        listCalls += 1;
        if (listCalls === 1) return ownerAList.promise;
        return Promise.resolve([{ id: "set-b", name: "owner-b-set", sessionIds: ["s1"], createdAt: 2, updatedAt: 2 }]);
      }
      return Promise.resolve(undefined);
    });
    const view = mountPanel();
    await flushUntil(() => listCalls === 1);

    act(() => {
      useAuth.setState({ user: account("owner-b") });
    });
    await flushUntil(() => view.container.textContent?.includes("owner-b-set") ?? false);

    await act(async () => {
      ownerAList.resolve([{ id: "set-a", name: "owner-a-set", sessionIds: ["s1"], createdAt: 1, updatedAt: 1 }]);
    });
    const text = view.container.textContent ?? "";
    expect(text).toContain("owner-b-set");
    expect(text).not.toContain("owner-a-set");
  });
});
