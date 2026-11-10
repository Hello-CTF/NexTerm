import { call } from "./commands";
import type { HostMonitorSnapshot } from "./generated/types";

export type {
  HostMonitorDisk,
  HostMonitorLoad,
  HostMonitorMemory,
  HostMonitorNetInterface,
  HostMonitorSnapshot,
} from "./generated/types";

export const hostMonitorApi = {
  sample: (sessionId: string): Promise<HostMonitorSnapshot> => call("host_monitor_sample", { sessionId }),
};
