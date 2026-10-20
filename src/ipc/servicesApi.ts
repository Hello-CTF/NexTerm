import { call } from "./commands";
import type {
  ActionRequest,
  FindRequest,
  FinderBatch,
  KillRequest,
  ListRequest,
  ServiceOperationBatch,
} from "./generated/types";

export type {
  ActionRequest,
  FindRequest,
  FinderBatch,
  FinderHostResult,
  FinderMatch,
  FinderQuery,
  KillRequest,
  ListRequest,
  Listener,
  Process,
  ServiceHostResult,
  ServiceOperationBatch,
  ServiceUnit,
} from "./generated/types";

export const servicesApi = {
  list: (request: ListRequest): Promise<ServiceOperationBatch> => call("services_list", request),
  action: (request: ActionRequest): Promise<ServiceOperationBatch> => call("services_action", request),
  find: (request: FindRequest): Promise<FinderBatch> => call("services_find", request),
  kill: (request: KillRequest): Promise<ServiceOperationBatch> => call("services_kill", request),
};
