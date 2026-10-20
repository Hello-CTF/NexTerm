/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { ExecBatch } from "../../ipc/execBatchApi";
import { click, clickButton, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

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
});
