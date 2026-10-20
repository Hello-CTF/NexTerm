import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call });
});

import { execBatchApi, getExecBatchConfirmationChallenge } from "../ipc/execBatchApi";

describe("execBatchApi", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("keeps the caller batch id on both parallel RPCs", async () => {
    mocks.call.mockResolvedValue({ batchId: "batch-1", results: [] });
    await execBatchApi.execute({
      batchId: "batch-1",
      sessionIds: ["s1", "s2"],
      command: "hostname",
      timeoutMs: 5000,
      maxConcurrent: 2,
    });
    expect(mocks.call).toHaveBeenNthCalledWith(1, "exec_batch_execute", {
      batchId: "batch-1",
      sessionIds: ["s1", "s2"],
      command: "hostname",
      timeoutMs: 5000,
      maxConcurrent: 2,
    });

    mocks.call.mockResolvedValue({ batchId: "batch-1", canceled: true });
    await execBatchApi.cancel("batch-1");
    expect(mocks.call).toHaveBeenNthCalledWith(2, "exec_batch_cancel", { batchId: "batch-1" });
  });

  it("forwards production confirmation and parses the machine-readable challenge", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.call.mockResolvedValue({ batchId: "batch-1", results: [] });
    await execBatchApi.execute({
      batchId: "batch-1",
      sessionIds: ["s1"],
      command: "deploy",
      productionConfirmation: { confirmed: true, targetSetHash, commandHash },
    });
    expect(mocks.call).toHaveBeenCalledWith("exec_batch_execute", {
      batchId: "batch-1",
      sessionIds: ["s1"],
      command: "deploy",
      timeoutMs: undefined,
      maxConcurrent: undefined,
      productionConfirmation: { confirmed: true, targetSetHash, commandHash },
    });

    const error = {
      code: "needs_confirm",
      message: "confirmation required",
      detail: {
        kind: "exec_batch_production_confirmation",
        batchId: "batch-1",
        targetSetHash,
        commandHash,
        productionTargets: [
          { sessionId: "s1", sessionName: "Production API", hostname: "api.prod.internal", environment: "production" },
        ],
      },
    };
    expect(getExecBatchConfirmationChallenge(error)).toEqual({
      kind: "exec_batch_production_confirmation",
      batchId: "batch-1",
      targetSetHash,
      commandHash,
      productionTargets: [
        { sessionId: "s1", sessionName: "Production API", hostname: "api.prod.internal", environment: "production" },
      ],
    });
    expect(getExecBatchConfirmationChallenge({ ...error, code: "internal" })).toBeNull();
    expect(
      getExecBatchConfirmationChallenge({
        ...error,
        detail: { ...error.detail, targetSetHash: "forged" },
      }),
    ).toBeNull();
  });
});
