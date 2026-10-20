import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("../ipc/commands", async () => {
  const { createFakeApi } = await import("./helpers/createFakeApi");
  return createFakeApi({ call: mocks.call });
});

import { execBatchApi } from "../ipc/execBatchApi";

describe("exec batch history API", () => {
  beforeEach(() => vi.clearAllMocks());

  it("forwards owner-scoped history pagination and matrix requests", async () => {
    mocks.call.mockResolvedValue({ items: [], total: 0, limit: 20, offset: 40 });
    await execBatchApi.historyList({ limit: 20, offset: 40 });
    expect(mocks.call).toHaveBeenNthCalledWith(1, "exec_batch_history_list", { limit: 20, offset: 40 });

    mocks.call.mockResolvedValue({ batch: { id: "batch-1" }, results: [] });
    await execBatchApi.historyGet("batch-1");
    expect(mocks.call).toHaveBeenNthCalledWith(2, "exec_batch_history_get", { batchId: "batch-1", scope: "self" });
    await execBatchApi.historyGet("batch-1", "ownerless");
    expect(mocks.call).toHaveBeenNthCalledWith(3, "exec_batch_history_get", { batchId: "batch-1", scope: "ownerless" });
  });
});
