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
}

export function cronTimeoutMs(job: CronJob): number {
  return Math.round(job.timeout / 1_000_000);
}

export const cronApi = {
  register: (args: CronRegistration) => call<CronJob>("cron_register", { args }),
  list: (sessionId: string) => call<CronJob[]>("cron_list", { sessionId }),
  get: (sessionId: string, jobId: string) =>
    call<CronJob>("cron_get", { sessionId, jobId }),
  setEnabled: (sessionId: string, jobId: string, enabled: boolean) =>
    call<CronJob>("cron_set_enabled", { sessionId, jobId, enabled }),
  unregister: (sessionId: string, jobId: string) =>
    call<void>("cron_unregister", { sessionId, jobId }),
};
