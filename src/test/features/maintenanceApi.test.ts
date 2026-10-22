import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn(), notify: vi.fn() }));
vi.mock("../../ipc/commands", () => ({ call: mocks.call }));
vi.mock("../../ipc/assetEvents", () => ({ notifyAssetDataChanged: mocks.notify }));

import { maintenanceApi } from "../../features/execbatch/maintenanceApi";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.call.mockImplementation((command: string) => Promise.resolve({ command }));
});

describe("maintenanceApi", () => {
  it("uses typed envelopes and notifies only after a successful asset edit", async () => {
    const editRequest = { targets: [{ assetId: "a", expectedUpdatedAt: 10 }], credentialId: "cred-1" };
    await maintenanceApi.bulkEdit(editRequest);
    expect(mocks.call).toHaveBeenNthCalledWith(1, "exec_batch_asset_edit", editRequest);
    expect(mocks.notify).toHaveBeenCalledTimes(1);

    const deployRequest = { batchId: "b", sessionIds: ["s"], publicKey: "ssh-ed25519 AAAA comment" };
    await maintenanceApi.deployPublicKey(deployRequest);
    expect(mocks.call).toHaveBeenNthCalledWith(2, "exec_batch_deploy_public_key", deployRequest);
    await maintenanceApi.testLogin(["a", "b"], 4000, 6);
    expect(mocks.call).toHaveBeenNthCalledWith(3, "exec_batch_test_login", {
      args: { assetIds: ["a", "b"], timeoutMs: 4000, maxConcurrent: 6 },
    });
    expect(mocks.notify).toHaveBeenCalledTimes(1);
  });

  it("does not emit an asset change notification when the edit RPC fails", async () => {
    mocks.call.mockRejectedValue(new Error("failed"));
    await expect(maintenanceApi.bulkEdit({ targets: [] })).rejects.toThrow("failed");
    expect(mocks.notify).not.toHaveBeenCalled();
  });
});
