import { call } from "./commands";

export interface CronJob {
  id: string;
  sessionId: string;
  name?: string;
  prompt: string;
  schedule: string;
  timezone: string;
  enabled: boolean;
  timeout: number;
  modelProfileId?: string;
  createdAt: string;
  updatedAt: string;
  revision: number;
  nextRunAt: string;
  retryAt?: string;
  circuitOpenUntil?: string;
  consecutiveFailures?: number;
  lastRunAt?: string;
  lastScheduledFor?: string;
  lastCoalesced?: boolean;
  lastError?: string;
  lease: {
    owner?: string;
    expiresAt?: string;
  };
  run: {
    id: string;
    scheduledFor: string;
    startedAt: string;
    deadline: string;
    coalesced?: boolean;
  };
}

export interface CronRegistration {
  sessionId: string;
  name?: string;
  prompt: string;
  schedule: string;
  timezone?: string;
  disabled?: boolean;
  timeoutMs?: number;
  modelProfileId?: string;
}

export function cronTimeoutMs(job: CronJob): number {
  return Math.round(job.timeout / 1_000_000);
}

export const cronApi = {
  register: (args: CronRegistration) => call("cron_register", { ...args }) as Promise<CronJob>,
  list: (sessionId: string) =>
    call("cron_list", { sessionId } as import("./generated/types").CronJobRequest) as Promise<CronJob[]>,
  get: (sessionId: string, jobId: string) =>
    call("cron_get", { sessionId, jobId }) as Promise<CronJob>,
  setEnabled: (sessionId: string, jobId: string, enabled: boolean) =>
    call("cron_set_enabled", { sessionId, jobId, enabled }) as Promise<CronJob>,
  unregister: (sessionId: string, jobId: string) =>
    call("cron_unregister", { sessionId, jobId }) as Promise<void>,
};
