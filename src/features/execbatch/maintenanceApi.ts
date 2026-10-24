import { call } from "../../ipc/commands";
import { notifyAssetDataChanged } from "../../ipc/assetEvents";
import type { ExecBatch } from "../../ipc/execBatchApi";
import type {
  AssetBulkEditIPCRequest,
  AssetProbeBatchDTO,
  DeployPublicKeyRequest,
} from "../../ipc/generated/types";

export type {
  AssetBulkEditIPCRequest,
  AssetBulkEditIPCResponse,
  AssetBulkEditIPCResult,
  AssetProbeBatchDTO,
  AssetProbeResultDTO,
  DeployPublicKeyRequest,
} from "../../ipc/generated/types";

export const maintenanceApi = {
  bulkEdit: async (request: AssetBulkEditIPCRequest) => {
    const response = await call("exec_batch_asset_edit", request);
    notifyAssetDataChanged();
    return response;
  },
  deployPublicKey: (request: DeployPublicKeyRequest): Promise<ExecBatch> =>
    call("exec_batch_deploy_public_key", request),
  testLogin: (assetIds: string[], timeoutMs = 10000, maxConcurrent = 8): Promise<AssetProbeBatchDTO> =>
    call("exec_batch_test_login", { args: { assetIds, timeoutMs, maxConcurrent } }),
};
