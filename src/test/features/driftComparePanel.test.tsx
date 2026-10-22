/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import type { ExecBatch } from "../../ipc/execBatchApi";
import { click, clickButton, flushMacrotask, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ execute: vi.fn(), cancel: vi.fn(), createId: vi.fn() }));

vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return { ...actual, execBatchApi: { execute: mocks.execute, cancel: mocks.cancel }, createExecBatchId: mocks.createId };
});

import { DriftComparePanel } from "../../features/execbatch/DriftComparePanel";
import type { TargetSetPickerTarget } from "../../features/execbatch/TargetSetPicker";

const targets: TargetSetPickerTarget[] = ["a", "b", "c"].map((id) => ({
  id,
  label: `host-${id}`,
  sessionName: `host-${id}`,
  endpoint: null,
  tags: [],
  workspaceNames: [],
  envCount: 0,
}));

let mounted: MountedView | undefined;

function result(sessionId: string, stdout: string) {
  return {
    sessionId,
    sessionName: `host-${sessionId}`,
    status: "success" as const,
    stdout,
    stderr: "",
    exitCode: 0,
    durationMs: 1,
    truncated: false,
  };
}

// The confirm click starts the async retry; keep an act scope open across a
// macrotask so its state updates land inside act.
async function clickAndFlush(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.createId.mockReturnValue("drift-batch");
  mocks.cancel.mockResolvedValue({ batchId: "drift-batch", canceled: true });
  const batch: ExecBatch = {
    batchId: "drift-batch",
    command: "cat < '/etc/a file'",
    startedAt: 1,
    finishedAt: 2,
    results: [result("a", "same\n"), result("b", "same\n"), result("c", "different\n")],
  };
  mocks.execute.mockResolvedValue(batch);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("DriftComparePanel", () => {
  it("executes a read-only file command, groups output and supports a host baseline diff", async () => {
    mounted = mount(createElement(DriftComparePanel, { targets }));
    for (const checkbox of mounted.container.querySelectorAll('input[type="checkbox"]')) click(checkbox);
    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="漂移文件路径"]');
    if (!input) throw new Error("drift path input not found");
    setInputValue(input, "/etc/a file");
    clickButton(mounted.container, "执行对比");
    await flushUntil(() => mounted?.container.querySelector('[data-testid="drift-diff"]') !== null);

    expect(mocks.execute).toHaveBeenCalledWith({
      batchId: "drift-batch",
      sessionIds: ["a", "b", "c"],
      command: "cat < '/etc/a file'",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
    expect(mounted.container.querySelectorAll("article")).toHaveLength(2);
    const diff = () => mounted?.container.querySelector('[aria-label="组间行级差异"]')?.textContent ?? "";
    expect(diff()).toContain("- same");
    expect(diff()).toContain("+ different");

    const pin = mounted.container.querySelector<HTMLButtonElement>('button[title="将 host-c 固定为 baseline"]');
    if (!pin) throw new Error("baseline pin not found");
    click(pin);
    expect(mounted.container.textContent).toContain("baseline：host-c");
    expect(diff()).toContain("- different");
    expect(diff()).toContain("+ same");
  });

  it("retries the M15 typed production challenge with the typed environment names", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.execute
      .mockRejectedValueOnce({
        code: "needs_confirm",
        detail: {
          kind: "exec_batch_production_confirmation",
          batchId: "drift-batch",
          targetSetHash,
          commandHash,
          requiredEnvironmentNames: ["production"],
          productionTargets: [{ sessionId: "a", sessionName: "host-a", environment: "production" }],
        },
      })
      .mockResolvedValueOnce({
        batchId: "drift-batch",
        command: "cat < '/etc/hostname'",
        startedAt: 1,
        finishedAt: 2,
        results: [result("a", "same\n")],
      });
    mounted = mount(createElement(DriftComparePanel, { targets }));
    click(mounted.container.querySelector('input[type="checkbox"]')!);
    clickButton(mounted.container, "执行对比");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);

    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]')!;
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 host-a"]');
    if (!environmentInput) throw new Error("typed environment input not found");
    const confirm = [...dialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === "确认执行（1）",
    );
    if (!confirm) throw new Error("confirm button not found");
    expect(confirm.disabled).toBe(true);
    setInputValue(environmentInput, "production");
    expect(confirm.disabled).toBe(false);
    await clickAndFlush(confirm);

    await flushUntil(() => mocks.execute.mock.calls.length === 2);
    expect(mocks.execute).toHaveBeenNthCalledWith(2, {
      batchId: "drift-batch",
      sessionIds: ["a"],
      command: "cat < '/etc/hostname'",
      timeoutMs: 30000,
      maxConcurrent: 4,
      productionConfirmation: { confirmed: true, targetSetHash, commandHash, environmentNames: ["production"] },
    });
  });
});
