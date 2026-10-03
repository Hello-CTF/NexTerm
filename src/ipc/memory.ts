// 长期语义记忆：Go 侧 memory_* 命令的类型化包装。
//
// 记忆是按 **owner scope**（tenant + subject）隔离的：每条命令都显式携带
// scope，内核逐行校验 owner —— 一个 scope 永远读不到、也改不到另一个 scope
// 的条目。前端不要缓存或推导 scope，调谁的作用域就传谁的。
//
// 两条能力都是 **opt-in**（默认关闭）：
// - `injectionEnabled`：开启后每次 AI 运行会把选中记忆作为一条临时系统消息
//   注入模型输入 —— 不写会话历史，下一轮重新注入；
// - `toolsEnabled`：开启后模型才能使用 memory_save / memory_list /
//   memory_recall / memory_forget 四个工具（计划模式与子代理不可用）。
//
// 所有写操作都是 CAS：必须带上读到的 `version`，不一致会被拒绝
// （`bad_param`，detail 里有 expected/actual），应重新读取后再提交。
import { call } from "./commands";

/** 记忆的 owner scope：条目按 (tenant, subject) 隔离。 */
export interface MemoryScope {
  tenant: string;
  subject: string;
}

export interface MemoryEntry {
  id: string;
  topic: string;
  content: string;
  /** 乐观锁版本号：写操作必须携带"我这份是基于哪个版本"。 */
  version: number;
  /** 内容在写入时被密钥规则脱敏过。 */
  redacted: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface MemoryIndexEntry {
  id: string;
  version: number;
  redacted: boolean;
  updatedAt: number;
}

export interface MemoryTopicIndex {
  topic: string;
  entries: MemoryIndexEntry[];
}

export interface MemorySettings {
  /** 记忆注入开关（默认 false）。 */
  injectionEnabled: boolean;
  /** 模型工具开关（默认 false）。 */
  toolsEnabled: boolean;
  version: number;
}

/**
 * 密钥处理策略。
 *
 * - `"reject"`（默认）：内容疑似包含密钥（password/token/api_key 等赋值）时
 *   整个写入被拒绝；
 * - `"redact"`：疑似密钥的值替换为 `[REDACTED]` 后正常保存。
 */
export type MemorySecretPolicy = "reject" | "redact";

export const memoryApi = {
  create: (scope: MemoryScope, topic: string, content: string, secrets?: MemorySecretPolicy) =>
    call<MemoryEntry>("memory_create", { scope, topic, content, secrets }),

  get: (scope: MemoryScope, id: string) =>
    call<MemoryEntry>("memory_get", { scope, id }),

  edit: (
    scope: MemoryScope,
    id: string,
    expectedVersion: number,
    patch: { topic?: string; content?: string; secrets?: MemorySecretPolicy },
  ) => call<MemoryEntry>("memory_edit", { scope, id, expectedVersion, ...patch }),

  delete: (scope: MemoryScope, id: string, expectedVersion: number) =>
    call<void>("memory_delete", { scope, id, expectedVersion }),

  index: (scope: MemoryScope) =>
    call<MemoryTopicIndex[]>("memory_index", { scope }),

  settings: (scope: MemoryScope) =>
    call<MemorySettings>("memory_settings_get", { scope }),

  /**
   * 更新 opt-in 开关（CAS）。`injectionEnabled` / `toolsEnabled` 至少给一个；
   * 缺省的字段保持原值。
   */
  setSettings: (
    scope: MemoryScope,
    expectedVersion: number,
    patch: { injectionEnabled?: boolean; toolsEnabled?: boolean },
  ) => call<MemorySettings>("memory_settings_set", { scope, expectedVersion, ...patch }),
};
