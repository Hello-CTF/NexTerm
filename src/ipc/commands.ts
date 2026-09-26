// 与 Rust 的唯一接口层：类型化命令包装（§6）。
// 类型来自 ts-rs 生成（cargo test 导出），见 types.ts。
import { invoke } from "@tauri-apps/api/core";
import { DEMO } from "../demo";

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
  return { code: "internal", message: String(e) };
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

export const fsApi = {
  list: (sessionId: string, path: string) =>
    call<import("./types").FileEntryDto[]>("fs_list", { sessionId, path }),
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
};

// ───────── mount ─────────

export const mountApi = {
  list: (forceRefresh?: boolean) =>
    call<import("./types").MountEntryDto[]>("mount_list", { forceRefresh }),
  create: (args: {
    sessionId: string;
    remotePath: string;
    localPoint: string;
    username?: string;
    password?: string;
  }) => call<import("./types").MountEntryDto>("mount_create", { args }),
  remove: (localPoint: string) => call<void>("mount_remove", { localPoint }),
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

export const aiApi = {
  chat: (
    args: {
      conversationId?: string;
      scope: Record<string, unknown>;
      message: string;
      selection?: string;
      channel: unknown;
    },
  ) => call<{ jobId: string }>("ai_chat", args),
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

// ───────── vault ─────────

export interface VaultStatus {
  initialized: boolean;
  mode: "not_init" | "dpapi" | "master";
  unlocked: boolean;
  autoLockMinutes: number;
}

export const vaultApi = {
  status: () => call<VaultStatus>("vault_status"),
  initMaster: (password: string) => call<void>("vault_init_master", { password }),
  initDpapi: () => call<void>("vault_init_dpapi"),
  unlock: (password: string) => call<void>("vault_unlock", { password }),
  lock: () => call<void>("vault_lock"),
  changePassword: (oldPassword: string, newPassword: string) =>
    call<void>("vault_change_password", { oldPassword, newPassword }),
  setCredential: (name: string, kind: string, secret: string, id?: string) =>
    call<{ id: string }>("vault_set_credential", {
      args: { id, name, kind, secret },
    }),
  listCredentials: () =>
    call<import("./types").CredentialMetaDto[]>("vault_list_credentials"),
  deleteCredential: (id: string) => call<void>("vault_delete_credential", { id }),
  revealCredential: (id: string) => call<string>("vault_reveal_credential", { id }),
  credentialSave: (name: string, kind: string, secret: string) =>
    call<{ id: string }>("credential_save", { name, kind, secret }),
};

// ───────── MCP ─────────

// ───────── MCP（M4-T1 设置 / M4-T2 写入外部工具）─────────

export interface McpSettings {
  enabled: boolean;
  httpPort: number;
  token: string;
  /** 逐工具写权限门；缺省即 false = 只读 */
  writeTools: Record<string, boolean>;
}

export interface McpToolDto {
  name: string;
  description: string;
  readOnly: boolean;
  writeEnabled: boolean;
}

export interface McpClientWriteResult {
  target: string;
  path: string;
  created: boolean;
  backup: string | null;
  snippet: Record<string, unknown>;
}

export const mcpApi = {
  getSettings: () => call<McpSettings>("mcp_get_settings"),
  /** 返回 restartRequired：true 表示端口/token/启停变了，需重启应用生效 */
  saveSettings: (settings: McpSettings) => call<boolean>("mcp_save_settings", { settings }),
  generateToken: () => call<string>("mcp_generate_token"),
  listTools: () => call<McpToolDto[]>("mcp_list_tools"),
  /** 一键写入外部 AI 工具配置（合并 + 备份，不覆盖其它 server） */
  writeClientConfig: (target: "claude_code" | "claude_desktop" | "cursor") =>
    call<McpClientWriteResult>("mcp_write_client_config", { target }),
  /** 兜底：拿片段自行粘贴 */
  clientConfigSnippet: () => call<Record<string, unknown>>("mcp_client_config_snippet"),
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
  list: () => call<import("./types").ForwardSpecDto[]>("forward_list"),
  remove: (id: string) => call<void>("forward_remove", { id }),
};
