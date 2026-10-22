/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { useUi } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import {
  click,
  clickButton,
  flushMacrotask,
  flushUntil,
  mount,
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

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    call: vi.fn(),
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
import { ProductionConfirmationDialog } from "../../features/execbatch/ProductionConfirmationDialog";

let mounted: MountedView | undefined;

function session(id: string, name: string): SessionInfo {
  return { id, assetId: `asset-${id}`, name, kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
}

const sessions = [session("s1", "ssh-one"), session("s2", "ssh-two")];

function challengeDetail(patch: Record<string, unknown> = {}) {
  return {
    kind: "exec_batch_production_confirmation",
    batchId: "batch-ui-test",
    targetSetHash: "a".repeat(64),
    commandHash: "b".repeat(64),
    requiredEnvironmentNames: ["production"],
    productionTargets: [
      { sessionId: "s1", sessionName: "ssh-one", hostname: "api.prod.internal", environment: "production" },
    ],
    ...patch,
  };
}

function mountPanel(): MountedView {
  mounted = mountWithClient(createElement(ExecBatchPanel, { sessions }));
  return mounted;
}

function dialog(): HTMLElement {
  const el = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
  if (!el) throw new Error("production dialog not found");
  return el;
}

function environmentInput(label: string): HTMLInputElement {
  const input = dialog().querySelector<HTMLInputElement>(`input[aria-label="输入环境名 ${label}"]`);
  if (!input) throw new Error(`environment input for ${label} not found`);
  return input;
}

function confirmButton(): HTMLButtonElement {
  const button = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.textContent?.trim() === "确认执行（1）",
  );
  if (!button) throw new Error("confirm button not found");
  return button;
}

// The confirm click starts the async retry; keep an act scope open across a
// macrotask so its state updates land inside act.
async function clickAndFlush(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
}

async function openTypedChallenge(): Promise<void> {
  mocks.execute.mockRejectedValueOnce({ code: "needs_confirm", detail: challengeDetail() });
  mountPanel();
  const checkbox = mounted!.container.querySelector<HTMLInputElement>('input[type="checkbox"]');
  if (!checkbox) throw new Error("target checkbox not found");
  click(checkbox);
  setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[aria-label="批量执行命令"]')!, "deploy --safe");
  clickButton(mounted!.container, "执行");
  await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ workspaces: [] });
  mocks.createId.mockReturnValue("batch-ui-test");
  mocks.assetList.mockResolvedValue([]);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("ProductionConfirmationDialog typed environment confirmation", () => {
  it("blocks confirmation until every target has its actual environment name", () => {
    mounted = mountWithClient(
      createElement(ProductionConfirmationDialog, {
        challenge: {
          targetSetHash: "a".repeat(64),
          requiredEnvironmentNames: ["production"],
          productionTargets: [
            { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
            { sessionId: "s2", sessionName: "two", hostname: "two.prod.internal", environment: "production" },
          ],
        },
        contentHash: "b".repeat(64),
        command: "deploy --safe",
        onCancel: () => undefined,
        onConfirm: () => undefined,
      }),
    );
    const confirm = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（2）",
    );
    if (!confirm) throw new Error("confirm button not found");
    expect(confirm.disabled).toBe(true);

    setInputValue(environmentInput("one.prod.internal"), "staging");
    expect(confirm.disabled).toBe(true);
    expect(dialog().textContent).toContain("环境名不匹配");

    setInputValue(environmentInput("one.prod.internal"), " production ");
    setInputValue(environmentInput("two.prod.internal"), "PRODUCTION");
    expect(confirm.disabled).toBe(false);
  });

  it("reports a cross-target mismatch and stays locked", () => {
    mounted = mountWithClient(
      createElement(ProductionConfirmationDialog, {
        challenge: {
          targetSetHash: "a".repeat(64),
          requiredEnvironmentNames: ["live", "production"],
          productionTargets: [
            { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
            { sessionId: "s2", sessionName: "two", hostname: "two.live.internal", environment: "live" },
          ],
        },
        contentHash: "b".repeat(64),
        command: "deploy --safe",
        onCancel: () => undefined,
        onConfirm: () => undefined,
      }),
    );
    const confirm = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（2）",
    );
    if (!confirm) throw new Error("confirm button not found");
    // Swapped names: each input is a real environment name but on the wrong target.
    setInputValue(environmentInput("one.prod.internal"), "live");
    setInputValue(environmentInput("two.live.internal"), "production");
    expect(confirm.disabled).toBe(true);
  });

  it("fails closed on extra inputs from an earlier challenge and unlocks after clearing them", () => {
    const view = mount(
      createElement(ProductionConfirmationDialog, {
        challenge: {
          targetSetHash: "a".repeat(64),
          requiredEnvironmentNames: ["live", "production"],
          productionTargets: [
            { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
            { sessionId: "s2", sessionName: "two", hostname: "two.live.internal", environment: "live" },
          ],
        },
        contentHash: "b".repeat(64),
        command: "deploy --safe",
        onCancel: () => undefined,
        onConfirm: () => undefined,
      }),
    );
    mounted = view;
    setInputValue(environmentInput("one.prod.internal"), "production");
    setInputValue(environmentInput("two.live.internal"), "live");

    // The target set shrinks without a remount (unkeyed call sites keep input state).
    act(() => {
      view.root.render(
        createElement(ProductionConfirmationDialog, {
          challenge: {
            targetSetHash: "d".repeat(64),
            requiredEnvironmentNames: ["production"],
            productionTargets: [
              { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
            ],
          },
          contentHash: "e".repeat(64),
          command: "deploy --safe",
          onCancel: () => undefined,
          onConfirm: () => undefined,
        }),
      );
    });
    const confirm = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("confirm button not found");
    expect(confirm.disabled).toBe(true);
    expect(dialog().textContent).toContain("不属于当前目标集合");

    clickButton(dialog(), "清除过期输入");
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput("one.prod.internal"), "production");
    expect(confirm.disabled).toBe(false);
  });

  it("locks confirmation with a visible error when required environment names are missing", () => {
    const onConfirm = vi.fn();
    mounted = mountWithClient(
      createElement(ProductionConfirmationDialog, {
        challenge: {
          targetSetHash: "a".repeat(64),
          productionTargets: [
            { sessionId: "s1", sessionName: "one", hostname: "one.prod.internal", environment: "production" },
          ],
        },
        contentHash: "b".repeat(64),
        command: "deploy --safe",
        onCancel: () => undefined,
        onConfirm,
      }),
    );

    expect(dialog().textContent).toContain("缺少必需的环境名");
    const confirm = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("confirm button not found");
    setInputValue(environmentInput("one.prod.internal"), "production");
    expect(confirm.disabled).toBe(true);
    expect(onConfirm).not.toHaveBeenCalled();
  });
});

describe("ExecBatchPanel typed production confirmation", () => {
  it("retries with the typed environment names after every target matches", async () => {
    mocks.execute
      .mockRejectedValueOnce({ code: "needs_confirm", detail: challengeDetail() })
      .mockResolvedValueOnce({ batchId: "batch-ui-test", command: "deploy --safe", startedAt: 1, finishedAt: 2, results: [] });
    await openTypedChallenge();

    const confirm = confirmButton();
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput("api.prod.internal"), "staging");
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput("api.prod.internal"), "production");
    expect(confirm.disabled).toBe(false);
    await clickAndFlush(confirm);

    await flushUntil(() => mocks.execute.mock.calls.length === 2);
    expect(mocks.execute).toHaveBeenNthCalledWith(2, {
      batchId: "batch-ui-test",
      sessionIds: ["s1"],
      command: "deploy --safe",
      timeoutMs: 30000,
      maxConcurrent: 4,
      productionConfirmation: {
        confirmed: true,
        targetSetHash: "a".repeat(64),
        commandHash: "b".repeat(64),
        environmentNames: ["production"],
      },
    });
  });

  it("cancel sends nothing and keeps the challenge closed", async () => {
    await openTypedChallenge();
    clickButton(dialog(), "取消");
    expect(document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]')).toBeNull();
    expect(mocks.execute).toHaveBeenCalledTimes(1);
    expect(mocks.cancel).not.toHaveBeenCalled();
  });

  it("editing the command discards the pending challenge (stale identity)", async () => {
    await openTypedChallenge();
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[aria-label="批量执行命令"]')!, "deploy --other");
    expect(document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]')).toBeNull();
    expect(mocks.execute).toHaveBeenCalledTimes(1);
  });
});
