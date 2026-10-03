// cron 无人值守定时任务：Go 侧 cron_* 命令的类型化包装。
//
// 任务的持久化与调度都在内核（internal/ai/cron）：注册/查询/启停/删除一律按
// sessionId + jobId 定位，切换会话只走 list，绝不会删除任何任务 —— 删除只有
// unregister 一条路径。无人值守执行在 guard 的 Unattended 模式下决策：需要人工
// 确认或被禁止的操作会被明确拒绝并如实记进任务的 lastError，不会静默执行，也
// 不会挂起等一个不会到来的人工确认。
import { call } from "./commands";

/**
 * 与 Go 侧 cron.Job 的 JSON 一一对应。
 *
 * 时间是 RFC3339 字符串；`timeout` 是 Go 的 time.Duration（JSON 为纳秒数），
 * 界面展示请用 {@link cronTimeoutMs} 换算。
 */
export interface CronJob {
  id: string;
  /** 所属 AI 会话 —— 到点即以该会话为 conversationId 发起无人值守执行。 */
  sessionId: string;
  name?: string;
  prompt: string;
  /** cron 表达式（5 段）。 */
  schedule: string;
  timezone: string;
  enabled: boolean;
  /** 单次执行超时（纳秒）。 */
  timeout: number;
  createdAt: string;
  updatedAt: string;
  /** 乐观锁版本号：get/list 返回的 revision 会随任务状态推进。 */
  revision: number;
  nextRunAt: string;
  retryAt?: string;
  circuitOpenUntil?: string;
  consecutiveFailures?: number;
  lastRunAt?: string;
  lastScheduledFor?: string;
  lastCoalesced?: boolean;
  /** 最近一次执行的错误（有界 2048 字符）；空串表示上一次成功。 */
  lastError?: string;
  lease: {
    owner?: string;
    expiresAt?: string;
  };
  /** 非空 id 表示已被认领、正在执行。 */
  run: {
    id: string;
    scheduledFor: string;
    startedAt: string;
    deadline: string;
    coalesced?: boolean;
  };
}

/** cron_register 的入参；timeoutMs 为毫秒，缺省用内核默认（1 分钟）。 */
export interface CronRegistration {
  sessionId: string;
  name?: string;
  prompt: string;
  schedule: string;
  /** 缺省 UTC。 */
  timezone?: string;
  /** true = 注册即停用（到点不执行，直到 setEnabled 打开）。 */
  disabled?: boolean;
  timeoutMs?: number;
}

/** CronJob.timeout（纳秒）转毫秒。 */
export function cronTimeoutMs(job: CronJob): number {
  return Math.round(job.timeout / 1_000_000);
}

export const cronApi = {
  /** 注册一个定时任务；sessionId 必须是已存在的 AI 会话。 */
  register: (args: CronRegistration) => call<CronJob>("cron_register", { args }),
  /** 列出某会话的任务 —— 切换会话只调用它，不触发任何删除。 */
  list: (sessionId: string) => call<CronJob[]>("cron_list", { sessionId }),
  get: (sessionId: string, jobId: string) =>
    call<CronJob>("cron_get", { sessionId, jobId }),
  /** 启停任务；重新启用时从当前时刻起算下一次执行，不补跑停用期间的点。 */
  setEnabled: (sessionId: string, jobId: string, enabled: boolean) =>
    call<CronJob>("cron_set_enabled", { sessionId, jobId, enabled }),
  /** 唯一的删除路径：必须同时携带会话与任务标识。 */
  unregister: (sessionId: string, jobId: string) =>
    call<void>("cron_unregister", { sessionId, jobId }),
};
