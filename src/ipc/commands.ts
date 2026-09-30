// 与 Rust 的唯一接口层：类型化命令包装（§6）。
// 类型来自 ts-rs 生成（cargo test 导出），见 types.ts。
import { invoke } from "@tauri-apps/api/core";
import { DEMO } from "../demo";
import { describeError } from "../ui/errorText";

// ───────── 通用 ─────────

export interface AppError {
  code: string;
  message: string;
  detail?: Record<string, unknown>;
}

export function toAppError(e: unknown): AppError {
  if (e && typeof e === "object" && "code" in e) {
    return e as AppError;
  }
  return { code: "internal", message: describeError(e) };
}

/**
 * 唯一的 IPC 出海口。演示模式下改走内存实现（`src/demo/mock.ts`），
 * 因此所有面板代码都不需要为"没有后端"这件事做任何适配。
 *
 * mock 用动态 import：让假数据 + 虚拟 shell 独立成一个 chunk，
 * Tauri 生产包不会加载它。
 */
async function call<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  try {
    if (DEMO) {
      const { mockInvoke } = await import("../demo/mock");
      return (await mockInvoke(cmd, args)) as T;
    }
    return await invoke<T>(cmd, args);
  } catch (e) {
    throw toAppError(e);
  }
}

// ───────── session ─────────

export interface SessionInfo {
  id: string;
  assetId: string | null;
  name: string;
  kind: string;
  status: "connecting" | "connected" | "reconnecting" | "disconnected" | "failed";
  tabs: string[];
  createdAt: number;
}

export const sessionApi = {
  connect: (assetId: string, acceptHostKey = false) =>
    call<SessionInfo>("session_connect", { args: { assetId, acceptHostKey } }),
  connectLocal: () => call<SessionInfo>("session_connect_local"),
  disconnect: (sessionId: string) =>
    call<void>("session_disconnect", { sessionId }),
  /**
   * 重连一个已断开的会话（§7）。
   *
   * **非阻塞**：返回 `true` 只代表"已开始重连"，真正的退避重试在后台跑（最坏
   * 三分钟），结果通过 `session://status` 事件推回来（重连中 → 已连接 / 连接失败）。
   * 返回 `false` 不是错误 —— 表示这个会话天然不可重连（本机会话没有重连语义，
   * 或没绑定资产）。
   */
  reconnect: (sessionId: string) => call<boolean>("session_reconnect", { sessionId }),
  list: () => call<SessionInfo[]>("session_list"),
  probe: (host: string, port: number, timeoutMs?: number) =>
    call<{ open: boolean; error?: string }>("session_probe", {
      args: { host, port, timeoutMs },
    }),
  openLineTab: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call<string>("session_open_line_tab", { sessionId, cols, rows, channel }),
  lineExec: (tabId: string, line: string) =>
    call<void>("session_line_exec", { tabId, line }),
  cwd: (sessionId: string) =>
    call<string | null>("session_cwd", { sessionId }),
};

// ───────── terminal ─────────

export const terminalApi = {
  attach: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call<string>("terminal_attach", { sessionId, cols, rows, channel }),
  write: (tabId: string, data: Uint8Array) =>
    call<void>("terminal_write", { args: { tabId, data: Array.from(data) } }),
  resize: (tabId: string, cols: number, rows: number) =>
    call<void>("terminal_resize", { tabId, cols, rows }),
  detach: (tabId: string) => call<void>("terminal_detach", { tabId }),
  screenText: (tabId: string) => call<string>("terminal_screen_text", { tabId }),
  snapshot: (tabId: string) =>
    call<import("./types").ScreenSnapshotDto>("terminal_snapshot", { tabId }),
  tail: (tabId: string, n: number) =>
    call<string[]>("terminal_tail", { tabId, n }),
  setVisible: (tabId: string, visible: boolean) =>
    call<void>("terminal_set_visible", { tabId, visible }),
  dump: (tabId: string, maxBytes?: number) =>
    call<string>("terminal_dump", { tabId, maxBytes }),
  switchEncoding: (tabId: string, encoding: string) =>
    call<void>("terminal_switch_encoding", { tabId, encoding }),
  recordStart: (tabId: string, path: string) =>
    call<void>("terminal_record_start", { tabId, path }),
  recordStop: (tabId: string) => call<number>("terminal_record_stop", { tabId }),
  /**
   * 把当前回滚输出写到本地文件，返回写入字节数。
   *
   * 走内核而不是前端：前端只装了 dialog 插件（能选路径），没装 fs 插件 ——
   * 浏览器里没有任何办法把一段文字落到用户磁盘上。
   */
  exportLog: (tabId: string, path: string, maxBytes?: number) =>
    call<number>("terminal_export_log", { tabId, path, maxBytes }),
  closeTab: (tabId: string) => call<void>("terminal_close_tab", { tabId }),
};

// ───────── asset / group / snippet ─────────

export interface Asset {
  id: string;
  groupId: string | null;
  kind: "ssh" | "winrm" | "local" | "docker" | "mysql" | "redis";
  name: string;
  host: string | null;
  port: number | null;
  username: string | null;
  authKind: string | null;
  keyPath: string | null;
  credId: string | null;
  options: Record<string, unknown>;
  tags: string;
  note: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
  deletedAt: number | null;
  /**
   * 应用自带的内置资产（「当前设备」）：不可删除，类型锁定，列表里带「本机」徽标。
   *
   * 判据来自内核（`asset.builtin` 列），不在前端按名字或 ID 猜 ——
   * 名字用户可以改，ID 常量写两份迟早会不一致。
   */
  builtin: boolean;
}

export interface AssetGroup {
  id: string;
  parentId: string | null;
  name: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
}

export const assetApi = {
  list: () => call<Asset[]>("asset_list"),
  get: (id: string) => call<Asset>("asset_get", { id }),
  create: (args: Partial<Asset> & { kind: string; name: string }) =>
    call<Asset>("asset_create", { args }),
  update: (args: { id: string } & Record<string, unknown>) =>
    call<Asset>("asset_update", { args }),
  /** 粘贴的私钥落成本地文件（应用数据目录 keys/），返回路径供绑定 keyPath。 */
  saveKeyFile: (content: string) =>
    call<{ path: string }>("asset_save_key_file", { content }),
  /** 读取用户选中的私钥文件内容（存入凭据库用，上限 64KB）。 */
  readKeyFile: (path: string) => call<string>("asset_read_key_file", { path }),
  delete: (id: string) => call<void>("asset_delete", { id }),
  search: (q: string) => call<Asset[]>("asset_search", { q }),
  groupList: () => call<AssetGroup[]>("group_list"),
  groupCreate: (name: string, parentId?: string) =>
    call<AssetGroup>("group_create", { name, parentId }),
  groupUpdate: (id: string, name?: string, parentId?: string | null) =>
    call<AssetGroup>("group_update", { id, name, parentId }),
  groupDelete: (id: string) => call<void>("group_delete", { id }),
  snippetList: () =>
    call<
      { id: string; name: string; body: string; groupId: string | null; sort: number }[]
    >("snippet_list"),
  snippetCreate: (name: string, body: string) =>
    call<{ id: string }>("snippet_create", { name, body }),
  snippetUpdate: (id: string, name: string, body: string) =>
    call<void>("snippet_update", { id, name, body }),
  snippetDelete: (id: string) => call<void>("snippet_delete", { id }),
  auditQuery: (args: Record<string, unknown> = {}) =>
    call<import("./types").AuditEntryDto[]>("audit_query", { args }),
  knownHostList: () => call<import("./types").KnownHostDto[]>("known_host_list"),
  knownHostAccept: (host: string, port: number, keyType: string, fingerprint: string) =>
    call<void>("known_host_accept", { host, port, keyType, fingerprint }),
  knownHostRemove: (id: string) => call<void>("known_host_remove", { id }),
  appInfo: () =>
    call<{ name: string; version: string; vault: import("./types").VaultStatusDto }>(
      "app_info",
    ),
};

// ───────── fs ─────────

/**
 * Windows 本地会话返回的路径是 `C:\Users\x\y`，而前端（左栏文件树、宽幅浏览器、
 * 面包屑）一律按 `/` 做路径算术。混用两种分隔符的后果是：`Users\WinCore` 被当成一段、
 * `parentOf` 找不到分隔符就跳回根。这里统一成 `/` ——
 * Windows 的文件 API 本来就接受正斜杠，回传也不会有问题。
 */
function normEntryPath<T extends { path: string }>(e: T): T {
  return { ...e, path: e.path.replace(/\\/g, "/") };
}

export const fsApi = {
  list: async (sessionId: string, path: string) => {
    const list = await call<import("./types").FileEntryDto[]>("fs_list", {
      sessionId,
      path,
    });
    return list.map(normEntryPath);
  },
  read: (sessionId: string, path: string, maxBytes?: number) =>
    call<{ path: string; size: number; contentBase64: string }>("fs_read", {
      sessionId,
      path,
      maxBytes,
    }),
  write: (
    sessionId: string,
    path: string,
    contentBase64: string,
    backup = true,
  ) => call<void>("fs_write", { args: { sessionId, path, contentBase64, backup } }),
  mkdir: (sessionId: string, path: string) =>
    call<void>("fs_mkdir", { sessionId, path }),
  rename: (sessionId: string, from: string, to: string) =>
    call<void>("fs_rename", { sessionId, from, to }),
  delete: (sessionId: string, path: string, isDir: boolean) =>
    call<void>("fs_delete", { sessionId, path, isDir }),
  chmod: (sessionId: string, path: string, mode: number) =>
    call<void>("fs_chmod", { sessionId, path, mode }),
  checksum: (sessionId: string, path: string, algo?: string) =>
    call<string>("fs_checksum", { sessionId, path, algo }),
  upload: (sessionId: string, localPath: string, remotePath: string, resume?: boolean) =>
    call<number>("fs_upload", { sessionId, localPath, remotePath, resume }),
  download: (sessionId: string, remotePath: string, localPath: string) =>
    call<number>("fs_download", { sessionId, remotePath, localPath }),
  /** 打包下载目录：远端 tar.gz → 本地文件，返回字节数。 */
  packDownload: (sessionId: string, remotePath: string, localPath: string) =>
    call<number>("fs_pack_download", { sessionId, remotePath, localPath }),
  /** 解压远端压缩包，返回真实落地目录。 */
  extract: (sessionId: string, path: string) =>
    call<string>("fs_extract", { sessionId, path }),
};

// ───────── mount ─────────

export const mountApi = {
  /** 本机是否支持磁盘挂载；返回原因（字符串）表示暂不可用。 */
  capability: () => call<string | null>("mount_capability"),
  list: (forceRefresh?: boolean) =>
    call<import("./types").MountEntryDto[]>("mount_list", { forceRefresh }),
  create: (args: {
    sessionId: string;
    remotePath: string;
    localPoint: string;
    username?: string;
    password?: string;
  }) => call<import("./types").MountEntryDto>("mount_create", { args }),
  remove: (localPoint: string, sessionId?: string) =>
    call<void>("mount_remove", { localPoint, sessionId }),
};

// ───────── docker ─────────

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  composeProject: string | null;
}

export interface ImageSummary {
  id: string;
  repository: string;
  tag: string;
  size: string;
  createdSince: string;
}

export const dockerApi = {
  overview: (sessionId: string) =>
    call<{ containers: ContainerSummary[]; hostStats: Record<string, number> }>(
      "docker_overview",
      { sessionId },
    ),
  ps: (sessionId: string) => call<ContainerSummary[]>("docker_ps", { sessionId }),
  logsAttach: (
    sessionId: string,
    containerId: string,
    tail: number,
    channel: unknown,
  ) => call<string>("docker_logs_attach", { sessionId, containerId, tail, channel }),
  execAttach: (
    sessionId: string,
    containerId: string,
    cols: number,
    rows: number,
    channel: unknown,
  ) =>
    call<string>("docker_exec_attach", {
      sessionId,
      containerId,
      cols,
      rows,
      channel,
    }),
  action: (sessionId: string, containerId: string, action: string, newName?: string) =>
    call<void>("docker_action", { args: { sessionId, containerId, action, newName } }),
  images: (sessionId: string) => call<ImageSummary[]>("docker_images", { sessionId }),
  imagePull: (sessionId: string, image: string) =>
    call<string>("docker_image_pull", { sessionId, image }),
  imageRemove: (sessionId: string, image: string, force?: boolean) =>
    call<void>("docker_image_remove", { sessionId, image, force }),
  inspect: (sessionId: string, containerId: string) =>
    call<Record<string, unknown>>("docker_inspect", { sessionId, containerId }),
  stats: (sessionId: string) => call<string>("docker_stats", { sessionId }),
  containerListDir: (sessionId: string, containerId: string, path: string) =>
    call<string[]>("docker_container_list_dir", { sessionId, containerId, path }),
};

// ───────── db ─────────

export interface QueryResult {
  columns: string[];
  rows: unknown[][];
  rowsAffected: number;
  durationMs: number;
  truncated: boolean;
  error: string | null;
}

/** 表字段（对应 Rust 侧 information_schema.columns 的投影）。 */
export interface TableColumn {
  name: string;
  type: string;
  nullable: boolean;
  key: string;
  default: string | null;
  extra: string;
}

/** 表索引（对应 information_schema.statistics）。 */
export interface TableIndex {
  name: string;
  unique: boolean;
  column: string;
  seq: number;
}

/** db_columns 的返回：字段 + 索引。 */
export interface TableDescribe {
  columns: TableColumn[];
  indexes: TableIndex[];
}

export const dbApi = {
  connect: (assetId?: string, inline?: Record<string, unknown>) =>
    call<{ connId: string }>("db_connect", { args: { assetId, inline } }),
  disconnect: (connId: string) => call<void>("db_disconnect", { connId }),
  schemas: (connId: string) => call<string[]>("db_schemas", { connId }),
  tables: (connId: string, schema?: string) =>
    call<string[]>("db_tables", { connId, schema }),
  columns: (connId: string, table: string, schema?: string) =>
    call<TableDescribe>("db_columns", { connId, table, schema }),
  query: (connId: string, sql: string, timeoutMs?: number) =>
    call<QueryResult>("db_query", { connId, sql, timeoutMs }),
  redisScan: (connId: string, cursor?: number, pattern?: string, count?: number) =>
    call<[number, string[]]>("redis_scan", { connId, cursor, pattern, count }),
  redisInspect: (connId: string, key: string) =>
    call<import("./types").RedisKeyViewDto>("redis_inspect", { connId, key }),
  redisCommand: (connId: string, args: string[]) =>
    call<string>("redis_command", { connId, args }),
  redisSetTtl: (connId: string, key: string, seconds: number) =>
    call<void>("redis_set_ttl", { connId, key, seconds }),
};

// ───────── ai ─────────

export interface ProviderConfig {
  baseUrl: string;
  apiKey: string;
  model: string;
  temperature: number;
  contextWindow: number;
  proxy: string | null;
  stream: boolean;
}

/** 常规 AI 的权限档位（终端接管是独立模块，不在这里）。 */
export type AiPermissionMode = "read_only" | "read_write" | "silent";

export interface AiPermissionConfig {
  mode: AiPermissionMode;
  /**
   * 自定义危险规则，**子串匹配**（大小写不敏感）。
   *
   * 命中即视为危险命令：任何档位都要问用户 —— 包括「完全静默」。
   */
  dangerRules: string[];
}

export const aiApi = {
  chat: (
    args: {
      conversationId?: string;
      scope: Record<string, unknown>;
      message: string;
      selection?: string;
      /** 图片附件（裸 base64 或 data URI）。 */
      images?: string[];
      /** 计划模式：只做只读调研并把方案交出来，等批准后再动手。 */
      planMode?: boolean;
      channel: unknown;
    },
  ) => {
    // 内核侧把请求体收成了单个 `args` 参数（那边平铺参数超了 clippy 上限 7），
    // channel 单独走顶层 —— Tauri 的 Channel 不能塞进普通结构体里。
    const { channel, ...body } = args;
    // 返回值必须带 `conversationId`：首轮是内核新建的会话，前端要把它记住，
    // 否则下一句追问会被当成新会话（现象：同一个会话里每追问一次就多一个历史项）。
    return call<{ jobId: string; conversationId: string }>("ai_chat", { args: body, channel });
  },
  /** 权限档位 + 自定义危险规则。 */
  getPermission: () => call<AiPermissionConfig>("ai_get_permission"),
  setPermission: (config: AiPermissionConfig) =>
    call<void>("ai_set_permission", { config }),
  cancel: (jobId: string) => call<void>("ai_cancel", { jobId }),
  confirm: (jobId: string, decision: "allow" | "allow_session" | "deny") =>
    call<void>("ai_confirm", { jobId, decision }),
  models: () => call<string[]>("ai_models"),
  testProvider: () =>
    call<{ modelsOk: boolean; modelsError?: string; chatOk: boolean; chatError?: string }>(
      "ai_test_provider",
    ),
  setProvider: (config: ProviderConfig) =>
    call<void>("ai_set_provider", { config }),
  getProvider: () => call<ProviderConfig>("ai_get_provider"),
  presets: () => call<string[]>("ai_presets"),
  takeoverEnter: (tabId: string, allowWrite: boolean) =>
    call<void>("ai_takeover_enter", { args: { tabId, allowWrite } }),
  takeoverExit: (tabId: string, reason?: string) =>
    call<void>("ai_takeover_exit", { tabId, reason }),
  takeoverRun: (args: {
    tabId: string;
    instruction: string;
    allowWrite: boolean;
    channel: unknown;
  }) => call<string>("ai_takeover_run", args),
  conversationList: () =>
    call<import("./types").ConversationDto[]>("ai_conversation_list"),
  conversationDelete: (id: string) => call<void>("ai_conversation_delete", { id }),
  conversationCreate: (title?: string) =>
    call<import("./types").ConversationDto>("ai_conversation_create", { title }),
  messages: (conversationId: string) =>
    call<import("./types").MessageDto[]>("ai_messages", { conversationId }),
};

// ───────── ai models（多模型档案，P0-3）─────────

// 类型由 ts-rs 从内核 `ai::profiles` 生成（跑 `cargo test` 导出到 types.ts）——
// 别在这里手抄一份，字段一改就两边漂移。
import type { ModelProfile, ModelProfilesView } from "./types";
export type { ModelProfile, ModelProfilesView };

export const modelApi = {
  overview: () =>
    call<ModelProfilesView | null>("ai_model_profiles").then(
      (v) => v ?? { profiles: [], activeId: null },
    ),
  save: (profile: ModelProfile) => call<ModelProfile>("ai_model_save", { profile }),
  remove: (id: string) => call<void>("ai_model_delete", { id }),
  activate: (id: string) => call<void>("ai_model_activate", { id }),
  /**
   * 按**表单当前值**去拉模型列表，不要求先保存 ——
   * 用户想先验证 baseUrl + key 能不能连，这个顺序必须支持。
   */
  refresh: (profile: ModelProfile) =>
    call<string[] | null>("ai_model_refresh", { profile }).then((v) => v ?? []),
  presets: () => call<string[] | null>("ai_presets").then((v) => v ?? []),
  preset: (name: string) => call<ModelProfile>("ai_model_preset", { preset: name }),
};

// ───────── vault ─────────

export interface VaultStatus {
  initialized: boolean;
  mode: "not_init" | "dpapi" | "master";
  unlocked: boolean;
  autoLockMinutes: number;
}

/** 「被谁使用」：凭据页的核心信息（asset.cred_id 反查）。 */
export interface CredentialUsedBy {
  id: string;
  name: string;
  kind: string;
}

/**
 * 私钥的来源。
 *
 * `inline` = 正文收进凭据库；`file` = 只记本地文件路径（引用，不复制内容）。
 * 口令永远跟私钥存在同一条凭据里，不是独立凭据。
 */
export type CredentialSource = "inline" | "file";

/** 凭据页用的列表项：Meta + 引用关系 + 私钥来源（后端返回 DTO 而不是 Row）。 */
export interface Credential {
  id: string;
  name: string;
  kind: string;
  createdAt: number;
  updatedAt: number;
  usedBy: CredentialUsedBy[];
  /** 仅私钥类有值；**锁定态为 null**（来源藏在密文里，解不开就不显示） */
  source: CredentialSource | null;
  /** 引用型私钥的本地路径 */
  refPath: string | null;
  hasPassphrase: boolean;
}

/** 凭据明文（「显示」用）。私钥类会把来源、路径、口令一并给出来。 */
export interface RevealedCredential {
  kind: string;
  /** 非私钥：值本身；内容型私钥：正文；引用型私钥：空串（库里本来就没有正文） */
  value: string;
  source: CredentialSource | null;
  refPath: string | null;
  passphrase: string | null;
}

export const vaultApi = {
  status: () => call<VaultStatus>("vault_status"),
  initMaster: (password: string) => call<void>("vault_init_master", { password }),
  initDpapi: () => call<void>("vault_init_dpapi"),
  unlock: (password: string) => call<void>("vault_unlock", { password }),
  lock: () => call<void>("vault_lock"),
  changePassword: (oldPassword: string, newPassword: string) =>
    call<void>("vault_change_password", { oldPassword, newPassword }),
  /**
   * 存凭据。
   *
   * `extra.source` 仅私钥有意义：`file` 时 `secret` 是**路径**（只记引用），
   * 否则是私钥正文。`extra.passphrase` 也是私钥专用，随私钥一起加密保存。
   */
  setCredential: (
    name: string,
    kind: string,
    secret: string,
    extra: { id?: string; source?: CredentialSource; passphrase?: string } = {},
  ) =>
    call<{ id: string }>("vault_set_credential", {
      args: {
        id: extra.id,
        name,
        kind,
        secret,
        source: extra.source,
        passphrase: extra.passphrase,
      },
    }),
  listCredentials: () => call<Credential[]>("vault_list_credentials"),
  deleteCredential: (id: string) => call<void>("vault_delete_credential", { id }),
  revealCredential: (id: string) => call<RevealedCredential>("vault_reveal_credential", { id }),
  /**
   * 改名 / 改值 / 改口令。
   *
   * ⚠️ `passphrase` 的语义是**提供即覆盖**（传空串 = 清除口令）；要"保持原样"就别带这个字段。
   * 只改口令时不必重新提供私钥 —— 后端会解出原载荷、只换口令再加密。
   */
  updateCredential: (
    id: string,
    patch: {
      name?: string;
      secret?: string;
      source?: CredentialSource;
      passphrase?: string;
    },
  ) => call<void>("credential_update", { args: { id, ...patch } }),
};

// ───────── port forward ─────────

export const forwardApi = {
  create: (sessionId: string, listenPort: number, targetHost: string, targetPort: number) =>
    call<import("./types").ForwardSpecDto>("forward_create", {
      sessionId,
      listenPort,
      targetHost,
      targetPort,
    }),
  /**
   * SOCKS5 动态转发：本地起一个 SOCKS5 代理，目标由客户端当场指定。
   *
   * 只有 SSH 会话能建（内置的代理无认证，所以内核只监听 127.0.0.1）。
   */
  createSocks: (sessionId: string, listenPort: number) =>
    call<import("./types").ForwardSpecDto>("forward_create_socks", {
      sessionId,
      listenPort,
    }),
  list: () => call<import("./types").ForwardSpecDto[]>("forward_list"),
  remove: (id: string) => call<void>("forward_remove", { id }),
};
