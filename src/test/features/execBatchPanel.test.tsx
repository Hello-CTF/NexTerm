/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { useUi, type Workspace } from "../../app/store";
import type { Asset, SessionInfo } from "../../ipc/commands";
import type { ExecBatch } from "../../ipc/execBatchApi";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushMacrotask,
  flushUntil,
  mountWithClient,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  execute: vi.fn(),
  cancel: vi.fn(),
  createId: vi.fn(),
  assetList: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    ...actual,
    call: vi.fn((command: string, _args?: unknown) => {
      if (command === "target_set_list") return Promise.resolve([]);
      if (command === "exec_batch_progress") {
        return Promise.resolve({
          batch: { batchId: "batch-ui-test", phase: "running", total: 1, pending: 0, running: 1, succeeded: 0, failed: 0, canceled: 0 },
          targets: [
            {
              sessionId: "s1",
              position: 0,
              phase: "running",
              output: {
                stdout: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
                stderr: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
              },
            },
          ],
        });
      }
      return Promise.resolve(undefined);
    }),
    assetApi: { list: mocks.assetList },
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
    execBatchApi: { execute: mocks.execute, cancel: mocks.cancel },
    createExecBatchId: mocks.createId,
  };
});

import { ExecBatchPanel } from "../../features/execbatch/ExecBatchPanel";

let mounted: MountedView | undefined;

function session(id: string, name: string, kind: string, status: SessionInfo["status"] = "connected"): SessionInfo {
  return { id, assetId: `asset-${id}`, name, kind, status, tabs: [], createdAt: 0 };
}

function asset(id: string, name: string, patch: Partial<Asset> = {}): Asset {
  return {
    id,
    groupId: null,
    kind: "ssh",
    name,
    host: null,
    port: 22,
    username: null,
    authKind: "agent",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 0,
    updatedAt: 0,
    deletedAt: null,
    builtin: false,
    ...patch,
  };
}

function workspace(id: string, title: string, sessionId: string): Workspace {
  const paneId = `pane-${id}`;
  return {
    id,
    kind: "session",
    title,
    sessionId,
    assetId: `asset-${sessionId}`,
    panes: [{ id: paneId, tabs: [], activeTabId: null }],
    activePaneId: paneId,
    splitRatio: 0.5,
    closable: true,
  };
}

const sessions = [
  session("s1", "ssh-one", "ssh"),
  session("s2", "ssh-two", "ssh"),
  session("s3", "ssh-offline", "ssh", "disconnected"),
  session("s4", "local-online", "local"),
  session("s5", "device-online", "device"),
  session("s6", "ssh-three", "ssh"),
  session("s7", "docker-online", "docker"),
];

function mountPanel(nextSessions: SessionInfo[] = sessions): MountedView {
  return mountWithClient(createElement(ExecBatchPanel, { sessions: nextSessions }));
}

function panel(): MountedView {
  if (!mounted) throw new Error("panel is not mounted");
  return mounted;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function commandInput(): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>('input[aria-label="批量执行命令"]');
  if (!input) throw new Error("command input not found");
  return input;
}

function searchInput(): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>('input[aria-label="搜索批量执行目标"]');
  if (!input) throw new Error("target search input not found");
  return input;
}

function targetCheckboxes(): HTMLInputElement[] {
  return [...(mounted?.container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]') ?? [])];
}

function visibleTargetIds(): string[] {
  return targetCheckboxes().map((checkbox) => checkbox.value);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ workspaces: [] });
  mocks.createId.mockReturnValue("batch-ui-test");
  mocks.cancel.mockResolvedValue({ batchId: "batch-ui-test", canceled: true });
  mocks.assetList.mockResolvedValue([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("ExecBatchPanel", () => {
  it("uses only connected SSH targets and renders distinct result states", async () => {
    const batch: ExecBatch = {
      batchId: "batch-ui-test",
      command: "hostname",
      startedAt: 1,
      finishedAt: 2,
      results: [
        {
          sessionId: "s1",
          sessionName: "ssh-one",
          status: "exit_error",
          stdout: "partial-out",
          stderr: "failed",
          exitCode: 17,
          durationMs: 8,
          truncated: true,
        },
        {
          sessionId: "s2",
          sessionName: "ssh-two",
          status: "transport_error",
          stdout: "",
          stderr: "",
          exitCode: null,
          error: "connection reset",
          durationMs: 9,
          truncated: false,
        },
      ],
    };
    mocks.execute.mockResolvedValue(batch);
    mounted = mountPanel();
    await flush();
    expect(visibleTargetIds()).toEqual(["s1", "s2", "s6"]);
    expect(text()).toContain("ssh-one");
    expect(text()).toContain("ssh-two");
    expect(text()).toContain("ssh-three");
    expect(text()).not.toContain("ssh-offline");
    expect(text()).not.toContain("local-online");
    expect(text()).not.toContain("device-online");
    expect(text()).not.toContain("docker-online");

    setInputValue(commandInput(), "hostname");
    clickButton(panel().container, "执行");
    await flushUntil(() => text().includes("请至少选择一个"));
    expect(mocks.execute).not.toHaveBeenCalled();

    click(targetCheckbox("s1"));
    click(targetCheckbox("s2"));
    expect(targetCheckbox("s6").checked).toBe(false);
    clickButton(panel().container, "执行");
    await flushUntil(() => text().includes("结果矩阵"));
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    expect(mocks.execute).toHaveBeenCalledWith({
      batchId: "batch-ui-test",
      sessionIds: ["s1", "s2"],
      command: "hostname",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
    expect(mocks.execute.mock.calls[0][0].sessionIds).not.toContain("s6");
    expect(text()).toContain("非零退出");
    expect(text()).toContain("传输错误");
    expect(text()).toContain("17");
    expect(text()).toContain("已截断");
    expect(text()).toContain("partial-out");
    expect(text()).toContain("connection reset");
  });

  it("cancels by batch id while the execute RPC is still pending", async () => {
    const pending = deferred<ExecBatch>();
    mocks.execute.mockReturnValue(pending.promise);
    mounted = mountPanel([sessions[0]]);
    await flush();
    click(targetCheckboxes()[0]);
    setInputValue(commandInput(), "sleep 60");
    clickButton(panel().container, "执行");
    await flush();
    await flushUntil(() => text().includes("正在执行批次 batch-ui-test"));

    clickButton(panel().container, "取消本批次");
    await flushUntil(() => mocks.cancel.mock.calls.length === 1 && text().includes("取消本批次"));
    expect(mocks.cancel).toHaveBeenCalledWith("batch-ui-test");
    expect(mocks.execute).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve({
        batchId: "batch-ui-test",
        command: "sleep 60",
        startedAt: 1,
        finishedAt: 2,
        results: [
          {
            sessionId: "s1",
            sessionName: "ssh-one",
            status: "canceled",
            stdout: "",
            stderr: "",
            exitCode: null,
            durationMs: 5,
            truncated: false,
          },
        ],
      });
      await flushMacrotask();
    });
    await flushUntil(() => text().includes("结果矩阵") && text().includes("已取消"));
    panel().unmount();
    mounted = undefined;
    expect(mocks.cancel).toHaveBeenCalledTimes(1);
  });

  function targetCheckbox(id: string): HTMLInputElement {
    const checkbox = mounted?.container.querySelector<HTMLInputElement>(`input[type="checkbox"][value="${id}"]`);
    if (!checkbox) throw new Error(`target checkbox not found: ${id}`);
    return checkbox;
  }

  async function mountMetadataPanel(): Promise<void> {
    mocks.assetList.mockResolvedValue([
      asset("asset-s1", "API 生产", {
        host: "api.internal",
        username: "deploy",
        tags: "production, 欧洲",
        options: { env: { APP_ENV: "production", SECRET_TOKEN: "hidden-env-value" } },
      }),
      asset("asset-s2", "数据库", {
        host: "db.internal",
        username: "root",
        tags: "production, mysql",
      }),
      asset("asset-s3", "跳板", {
        host: "jump.internal",
        username: "ops",
        tags: "staging",
      }),
    ]);
    useUi.setState({
      workspaces: [
        workspace("ws-1", "工作区甲", "s1"),
        workspace("ws-2", "工作区乙", "s2"),
      ],
    });
    mounted = mountPanel([
      session("s1", "session-one", "ssh"),
      session("s2", "session-two", "ssh"),
      session("s3", "session-three", "ssh"),
    ]);
    await flushUntil(() => text().includes("deploy@api.internal") && text().includes("工作区甲"));
  }

  it("shows target metadata and filters by name, host, and tag", async () => {
    await mountMetadataPanel();
    expect(text()).toContain("API 生产");
    expect(text()).toContain("会话：session-one");
    expect(text()).toContain("deploy@api.internal");
    expect(text()).toContain("工作区：工作区甲");
    expect(text()).toContain("标签：production、欧洲");
    expect(text()).toContain("env 2");
    expect(text()).not.toContain("hidden-env-value");

    setInputValue(searchInput(), "API 生产");
    expect(visibleTargetIds()).toEqual(["s1"]);
    setInputValue(searchInput(), "db.internal");
    expect(visibleTargetIds()).toEqual(["s2"]);
    setInputValue(searchInput(), "MYSQL");
    expect(visibleTargetIds()).toEqual(["s2"]);
    setInputValue(searchInput(), "欧洲");
    expect(visibleTargetIds()).toEqual(["s1"]);
    setInputValue(searchInput(), "not-a-target");
    expect(visibleTargetIds()).toEqual([]);
    expect(text()).toContain("没有匹配当前搜索");
  });

  it("merges filtered selection with hidden selections and executes exactly the merged targets", async () => {
    await mountMetadataPanel();
    click(targetCheckbox("s1"));
    click(targetCheckbox("s3"));
    expect(text()).toContain("目标会话（已选 2）");
    expect(targetCheckbox("s1").checked).toBe(true);
    expect(targetCheckbox("s3").checked).toBe(true);

    clickButton(panel().container, "清空");
    expect(text()).toContain("目标会话（已选 0）");
    click(targetCheckbox("s3"));
    setInputValue(searchInput(), "production");
    expect(visibleTargetIds()).toEqual(["s1", "s2"]);
    expect(text()).toContain("目标会话（已选 1）");
    clickButton(panel().container, "全选筛选结果");
    expect(text()).toContain("目标会话（已选 3）");
    expect(targetCheckbox("s1").checked).toBe(true);
    expect(targetCheckbox("s2").checked).toBe(true);

    setInputValue(searchInput(), "");
    expect(targetCheckboxes().every((checkbox) => checkbox.checked)).toBe(true);
    mocks.execute.mockResolvedValue({
      batchId: "batch-ui-test",
      command: "hostname",
      startedAt: 1,
      finishedAt: 2,
      results: [],
    });
    setInputValue(commandInput(), "hostname");
    clickButton(panel().container, "执行");
    await flushUntil(() => text().includes("结果矩阵"));
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    expect(mocks.execute).toHaveBeenCalledWith({
      batchId: "batch-ui-test",
      sessionIds: ["s1", "s2", "s3"],
      command: "hostname",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });

    clickButton(panel().container, "清空");
    expect(text()).toContain("目标会话（已选 0）");
    expect(targetCheckboxes().every((checkbox) => !checkbox.checked)).toBe(true);
  });

  it("falls back to session-name search, selection, and execution when asset metadata fails", async () => {
    mocks.assetList.mockRejectedValue(new Error("asset list unavailable"));
    mocks.execute.mockResolvedValue({
      batchId: "batch-ui-test",
      command: "hostname",
      startedAt: 1,
      finishedAt: 2,
      results: [],
    });
    mounted = mountPanel([
      session("s1", "fallback-ssh", "ssh"),
      session("s2", "other-ssh", "ssh"),
    ]);
    await flushUntil(() => text().includes("资产详情加载失败") && text().includes("asset list unavailable"));
    expect(text()).toContain("别名、主机与标签搜索可能不完整");
    expect(visibleTargetIds()).toEqual(["s1", "s2"]);

    setInputValue(searchInput(), "fallback-ssh");
    expect(visibleTargetIds()).toEqual(["s1"]);
    click(targetCheckbox("s1"));
    setInputValue(commandInput(), "hostname");
    clickButton(panel().container, "执行");
    await flushUntil(() => text().includes("结果矩阵"));
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    expect(mocks.execute).toHaveBeenCalledWith({
      batchId: "batch-ui-test",
      sessionIds: ["s1"],
      command: "hostname",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
  });

  it("discards the query and selection when the panel closes", async () => {
    await mountMetadataPanel();
    click(targetCheckbox("s1"));
    setInputValue(searchInput(), "production");
    expect(text()).toContain("目标会话（已选 1）");

    panel().unmount();
    mounted = undefined;
    await mountMetadataPanel();

    expect(searchInput().value).toBe("");
    expect(text()).toContain("目标会话（已选 0）");
    expect(targetCheckboxes().every((checkbox) => !checkbox.checked)).toBe(true);
  });

  it("opens backend production confirmation and retries with the confirmed hashes", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.execute
      .mockRejectedValueOnce({
        code: "needs_confirm",
        message: "生产环境批量执行需要显式确认",
        detail: {
          kind: "exec_batch_production_confirmation",
          batchId: "batch-ui-test",
          targetSetHash,
          commandHash,
          requiredEnvironmentNames: ["production"], productionTargets: [
            { sessionId: "s1", sessionName: "ssh-one", hostname: "api.prod.internal", environment: "production" },
            { sessionId: "s2", sessionName: "ssh-two", hostname: "db.prod.internal", environment: "production" },
          ],
        },
      })
      .mockResolvedValueOnce({
        batchId: "batch-ui-test",
        command: "deploy --safe",
        startedAt: 1,
        finishedAt: 2,
        results: [],
      });
    mounted = mountPanel([sessions[0], sessions[1]]);
    for (const checkbox of targetCheckboxes()) click(checkbox);
    setInputValue(commandInput(), "deploy --safe");
    clickButton(panel().container, "执行");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);

    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
    if (!dialog) throw new Error("production confirmation dialog not found");
    expect(dialog.textContent).toContain("api.prod.internal");
    expect(dialog.textContent).toContain("db.prod.internal");
    expect(dialog.textContent).toContain("deploy --safe");
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "确认执行（2）",
    );
    const firstEnvironment = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 api.prod.internal"]');
    const secondEnvironment = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 db.prod.internal"]');
    if (!confirm || !firstEnvironment || !secondEnvironment) throw new Error("production confirmation controls not found");
    expect(confirm.disabled).toBe(true);
    setInputValue(firstEnvironment, "production");
    setInputValue(secondEnvironment, "production");
    expect(confirm.disabled).toBe(false);
    click(confirm);

    await flushUntil(() => mocks.execute.mock.calls.length === 2 && text().includes("结果矩阵"));
    expect(mocks.execute).toHaveBeenNthCalledWith(2, {
      batchId: "batch-ui-test",
      sessionIds: ["s1", "s2"],
      command: "deploy --safe",
      timeoutMs: 30000,
      maxConcurrent: 4,
      productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["production"] },
    });
  });

  function productionDialog(): HTMLElement {
    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
    if (!dialog) throw new Error("production confirmation dialog not found");
    return dialog;
  }

  async function mountProductionDialog(command = "deploy --safe", commandHash = "b".repeat(64)): Promise<HTMLElement> {
    mocks.execute.mockRejectedValueOnce({
      code: "needs_confirm",
      message: "生产环境批量执行需要显式确认",
      detail: {
        kind: "exec_batch_production_confirmation",
        batchId: "batch-ui-test",
        targetSetHash: "a".repeat(64),
        commandHash,
        requiredEnvironmentNames: ["production"], productionTargets: [
          { sessionId: "s1", sessionName: "ssh-one", hostname: "api.prod.internal", environment: "production" },
        ],
      },
    });
    mounted = mountPanel([sessions[0]]);
    click(targetCheckbox("s1"));
    setInputValue(commandInput(), command);
    clickButton(panel().container, "执行");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);
    return productionDialog();
  }

  function expectClosedWithoutExecution(): void {
    expect(document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]')).toBeNull();
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(text()).not.toContain("结果矩阵");
  }

  it("cancel closes production confirmation after exactly one rejected execute and no result matrix", async () => {
    const dialog = await mountProductionDialog();
    clickButton(dialog, "取消");
    expectClosedWithoutExecution();
  });

  it("Escape closes production confirmation after exactly one rejected execute and no result matrix", async () => {
    await mountProductionDialog();
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expectClosedWithoutExecution();
  });

  it("overlay click closes production confirmation after exactly one rejected execute and no result matrix", async () => {
    const dialog = await mountProductionDialog();
    const overlay = dialog.parentElement;
    if (!overlay?.classList.contains("nx-overlay")) throw new Error("production overlay not found");
    click(overlay);
    expectClosedWithoutExecution();
  });

  it("invalidates a pending production challenge when the command is edited", async () => {
    await mountProductionDialog("deploy --safe", "b".repeat(64));
    mocks.execute.mockRejectedValueOnce({
      code: "needs_confirm",
      message: "生产环境批量执行需要显式确认",
      detail: {
        kind: "exec_batch_production_confirmation",
        batchId: "batch-ui-test",
        targetSetHash: "a".repeat(64),
        commandHash: "c".repeat(64),
        requiredEnvironmentNames: ["production"], productionTargets: [
          { sessionId: "s1", sessionName: "ssh-one", hostname: "api.prod.internal", environment: "production" },
        ],
      },
    });

    setInputValue(commandInput(), "deploy --changed");
    expect(document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]')).toBeNull();
    clickButton(panel().container, "执行");
    await flushUntil(
      () => mocks.execute.mock.calls.length === 2 &&
        document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null,
    );

    expect(mocks.execute).toHaveBeenCalledTimes(2);
    expect(mocks.execute.mock.calls[1][0]).toMatchObject({ command: "deploy --changed" });
    expect(mocks.execute.mock.calls[1][0].productionConfirmation).toBeUndefined();
    expect(productionDialog().textContent).toContain("deploy --changed");
    expect(text()).not.toContain("结果矩阵");
  });

  function variablePrompt(): HTMLElement {
    const dialog = [...document.body.querySelectorAll<HTMLElement>('[role="dialog"]')].find(
      (candidate) => candidate.textContent?.includes("填写批量执行变量"),
    );
    if (!dialog) throw new Error("variable prompt not found");
    return dialog;
  }

  it("renders asset built-ins separately for every target before executing grouped requests", async () => {
    mocks.assetList.mockResolvedValue([
      asset("asset-s1", "api-a", { host: "10.0.0.1", tags: "production, web" }),
      asset("asset-s2", "api-b", { host: "10.0.0.2", tags: "staging，worker" }),
    ]);
    mocks.execute.mockResolvedValue({
      batchId: "batch-ui-test",
      command: "rendered",
      startedAt: 1,
      finishedAt: 2,
      results: [],
    });
    mounted = mountPanel([sessions[0], sessions[1]]);
    await flushUntil(() => text().includes("api-a") && text().includes("api-b"));
    click(targetCheckbox("s1"));
    click(targetCheckbox("s2"));
    setInputValue(commandInput(), "echo {{asset.name}} {{asset.host}} {{asset.tags}} {{service=nginx}}");
    clickButton(panel().container, "执行");
    await flushUntil(() => document.body.textContent?.includes("填写批量执行变量") ?? false);

    const prompt = variablePrompt();
    expect(prompt.querySelectorAll("input, textarea")).toHaveLength(1);
    const service = prompt.querySelector<HTMLInputElement>('input[aria-label="变量 service"]');
    if (!service) throw new Error("service input not found");
    expect(service.value).toBe("nginx");
    expect(mocks.execute).not.toHaveBeenCalled();
    clickButton(prompt, "继续");
    await flushUntil(() => mocks.execute.mock.calls.length === 2 && text().includes("结果矩阵"));

    const requests = mocks.execute.mock.calls.map(([request]) => request);
    const first = requests.find((request) => request.sessionIds.includes("s1"));
    const second = requests.find((request) => request.sessionIds.includes("s2"));
    expect(first).toMatchObject({
      sessionIds: ["s1"],
      command: "echo api-a 10.0.0.1 production, web nginx",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
    expect(second).toMatchObject({
      sessionIds: ["s2"],
      command: "echo api-b 10.0.0.2 staging, worker nginx",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
  });

  it("shows and retries the rendered command for production confirmation", async () => {
    const rendered = "deploy api; printf '%s' \"$HOME\"";
    const targetSetHash = "a".repeat(64);
    const commandHash = "d".repeat(64);
    mocks.execute
      .mockRejectedValueOnce({
        code: "needs_confirm",
        message: "生产环境批量执行需要显式确认",
        detail: {
          kind: "exec_batch_production_confirmation",
          batchId: "batch-ui-test",
          targetSetHash,
          commandHash,
          requiredEnvironmentNames: ["production"], productionTargets: [
            { sessionId: "s1", sessionName: "ssh-one", hostname: "api.prod.internal", environment: "production" },
          ],
        },
      })
      .mockResolvedValueOnce({
        batchId: "batch-ui-test",
        command: rendered,
        startedAt: 1,
        finishedAt: 2,
        results: [],
      });
    mounted = mountPanel([sessions[0]]);
    await flush();
    click(targetCheckbox("s1"));
    setInputValue(commandInput(), "deploy {{service}}");
    clickButton(panel().container, "执行");
    await flushUntil(() => document.body.textContent?.includes("填写批量执行变量") ?? false);
    const service = variablePrompt().querySelector<HTMLInputElement>('input[aria-label="变量 service"]');
    if (!service) throw new Error("service input not found");
    setInputValue(service, "api; printf '%s' \"$HOME\"");
    clickButton(variablePrompt(), "继续");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);

    const dialog = productionDialog();
    expect(mocks.execute.mock.calls[0][0]).toMatchObject({ command: rendered });
    expect(dialog.textContent).toContain(rendered);
    expect(dialog.textContent).not.toContain("{{service}}");
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 api.prod.internal"]');
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "确认执行（1）",
    );
    if (!environmentInput || !confirm) throw new Error("production confirmation controls not found");
    setInputValue(environmentInput, "production");
    click(confirm);
    await flushUntil(() => mocks.execute.mock.calls.length === 2 && text().includes("结果矩阵"));
    expect(mocks.execute).toHaveBeenNthCalledWith(2, {
      batchId: "batch-ui-test",
      sessionIds: ["s1"],
      command: rendered,
      timeoutMs: 30000,
      maxConcurrent: 4,
      productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["production"] },
    });
  });

  it("reports unknown variables without opening a prompt or executing", async () => {
    mounted = mountPanel([sessions[0]]);
    await flush();
    click(targetCheckbox("s1"));
    setInputValue(commandInput(), "echo {{asset.environment}}");
    clickButton(panel().container, "执行");
    await flushUntil(() => text().includes("未知变量"));

    expect(text()).toContain("asset.environment");
    expect(document.body.textContent).not.toContain("填写批量执行变量");
    expect(mocks.execute).not.toHaveBeenCalled();
  });
});
