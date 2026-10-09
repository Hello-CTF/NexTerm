import { WEB } from "./env";
import { clientId, httpUrl } from "./env";
import { describeError } from "../ui/errorText";
import { authedFetch } from "./serverAuth";
import { callDesktop } from "./wails";

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

export async function call<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  try {
    if (WEB) {
      return await callWeb<T>(cmd, args);
    }
    return await callDesktop<T>(cmd, args);
  } catch (e) {
    throw toAppError(e);
  }
}

const CSRF_HEADER = "X-NexTerm-CSRF";

// auth=on 下服务端对 cookie 会话的一切 POST(含 /rpc 读命令)强制校验 CSRF 头,
// 拒绝形状固定为 403 + { code:"forbidden", message 含 "CSRF" }。
function isCsrfRejection(status: number, text: string): boolean {
  if (status !== 403) return false;
  try {
    const body = JSON.parse(text) as { error?: { code?: string; message?: string } } | null;
    return (
      body?.error?.code === "forbidden" &&
      typeof body.error.message === "string" &&
      body.error.message.includes("CSRF")
    );
  } catch {
    return false;
  }
}

function postRpc(payload: string, csrf: string | null): Promise<Response> {
  return authedFetch(httpUrl("/rpc"), {
    method: "POST",
    headers: {
      "content-type": "application/json",
      ...(csrf ? { [CSRF_HEADER]: csrf } : {}),
    },
    body: payload,
  });
}

async function callWeb<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const { channel, clientId: stableClientId, ...requestBody } = args ?? {};
  const requestPayload = JSON.stringify({
    cmd,
    args: args ? requestBody : null,
    ...(channel === undefined ? {} : { channel }),
    ...(stableClientId === undefined ? {} : { clientId: stableClientId }),
  });
  // 延迟加载 authApi: commands 与 authApi 互相引用, 动态加载避免静态环。
  const { authApi, getCsrfToken } = await import("./authApi");
  let res = await postRpc(requestPayload, getCsrfToken());
  let text = await res.text();
  if (isCsrfRejection(res.status, text)) {
    // 会话可能已轮换导致旧令牌失效:经 /auth/me 刷新并重试同一请求,只此一次。
    // CSRF 拒绝发生在中间件、未触达命令 handler,重试不会产生重复副作用。
    await authApi.me();
    res = await postRpc(requestPayload, getCsrfToken());
    text = await res.text();
  }
  let body: { ok?: boolean; data?: unknown; error?: unknown } | null = null;
  try {
    body = JSON.parse(text) as { ok?: boolean; data?: unknown; error?: unknown };
  } catch {
    throw {
      code: "internal",
      message: `服务端返回了非 JSON 响应（HTTP ${res.status}）：${text.slice(0, 120)}`,
    } satisfies AppError;
  }
  if (!body || body.ok !== true) {
    throw (body?.error ?? { code: "internal", message: `HTTP ${res.status}` }) as AppError;
  }
  return body.data as T;
}

export const systemApi = {
  platform: () => call<string>("app_platform"),
};

import type { UpdateResultDto, UpdateStatusDto } from "./types";

export const appUpdateApi = {
  check: () => call<UpdateStatusDto>("app_update_check"),
  install: (version?: string) =>
    call<UpdateResultDto>("app_update_install", { version: version ?? null }),
  restart: () => call<UpdateResultDto>("app_restart"),
};

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
  connectQuick: (args: {
    host: string;
    port: number;
    username?: string;
    authKind: "password" | "agent";
    password?: string;
    acceptHostKey?: boolean;
  }) => call<SessionInfo>("session_connect_quick", { args }),
  quickConnectDefaultUser: () => call<string>("session_quick_connect_user"),
  connectLocal: () => call<SessionInfo>("session_connect_local"),
  disconnect: (sessionId: string) =>
    call<void>("session_disconnect", { sessionId }),
  reconnect: (sessionId: string) => call<boolean>("session_reconnect", { sessionId }),
  list: () => call<SessionInfo[]>("session_list"),
  probe: (host: string, port: number, timeoutMs?: number) =>
    call<{ open: boolean; error?: string }>("session_probe", {
      args: { host, port, timeoutMs },
    }),
  probeHostKey: (assetId: string, timeoutMs?: number) =>
    call<import("./types").SshHostKeyProbeDto>("session_probe_host_key", {
      args: { assetId, timeoutMs },
    }),
  openLineTab: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call<string>("session_open_line_tab", {
      sessionId,
      cols,
      rows,
      channel,
      clientId: clientId(),
    }),
  lineExec: (tabId: string, line: string) =>
    call<void>("session_line_exec", { tabId, line }),
  cwd: (sessionId: string) =>
    call<string | null>("session_cwd", { sessionId }),
};

export interface AttachedTabInfo {
  tabId: string;
  sessionId: string;
  cols: number;
  rows: number;
  controller: string | null;
  subscribers: number;
  viewers: number;
  exited: boolean;
  encoding?: string;
}

export interface LiveTabInfo {
  tabId: string;
  sessionId: string;
  sessionName: string;
  sessionKind: string;
  cols: number;
  rows: number;
  controller: string | null;
  subscribers: number;
  viewers: number;
  exited: boolean;
  lastOutputMsAgo: number;
}

export const terminalApi = {
  attach: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call<string>("terminal_attach", { sessionId, cols, rows, channel, clientId: clientId() }),
  attachTab: (tabId: string, channel: unknown, replayBytes?: number) =>
    call<AttachedTabInfo>("terminal_attach_tab", {
      tabId,
      replayBytes,
      channel,
      clientId: clientId(),
    }),
  write: (tabId: string, data: Uint8Array) =>
    call<void>("terminal_write", {
      args: { tabId, data: Array.from(data), clientId: clientId() },
    }),
  resize: (tabId: string, cols: number, rows: number) =>
    call<void>("terminal_resize", { tabId, cols, rows, clientId: clientId() }),
  resizeFlush: (tabId: string) =>
    call<void>("terminal_resize_flush", { tabId, clientId: clientId() }),
  detach: (tabId: string, channelId?: string) =>
    call<void>("terminal_detach", { tabId, channelId }),
  claim: (tabId: string) =>
    call<string | null>("terminal_claim", { tabId, clientId: clientId() }),
  release: (tabId: string) =>
    call<boolean>("terminal_release", { tabId, clientId: clientId() }),
  listLive: () => call<LiveTabInfo[]>("terminal_list"),
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
  exportLog: (tabId: string, path: string, maxBytes?: number) =>
    call<number>("terminal_export_log", { tabId, path, maxBytes }),
  closeTab: (tabId: string, mode?: "kill" | "detach") =>
    call<void>("terminal_close_tab", { tabId, mode, clientId: clientId() }),
};

export interface TranscriptSummary {
  id: string;
  sessionId: string;
  assetId: string;
  assetName: string;
  assetKind: string;
  assetDeleted: boolean;
  startedAt: number;
  endedAt: number | null;
  bytes: number;
  chunks: number;
  truncated: boolean;
  active: boolean;
  syncOptIn?: boolean;
  contentOmitted?: boolean;
}

export interface TranscriptChunk {
  seq: number;
  tabId: string;
  ts: number;
  kind?: number;
  dataBase64: string;
}

export interface TranscriptReadResult {
  chunks: TranscriptChunk[];
  nextSeq: number;
  done: boolean;
  totalBytes: number;
}

export interface TranscriptMatch {
  seq: number;
  ts: number;
  preview: string;
}

export interface TranscriptHost {
  assetId: string;
  assetName: string;
  assetKind: string;
  assetDeleted: boolean;
  transcripts: number;
  lastStartedAt: number;
}

export const transcriptApi = {
  hosts: () => call<TranscriptHost[]>("transcript_hosts"),
  list: (assetId: string) =>
    call<TranscriptSummary[]>("transcript_list", { assetId }),
  read: (id: string, afterSeq = 0, maxBytes?: number) =>
    call<TranscriptReadResult>("transcript_read", { id, afterSeq, maxBytes }),
  search: (id: string, query: string) =>
    call<TranscriptMatch[]>("transcript_search", { id, query }),
  remove: (id: string) => call<void>("transcript_delete", { id }),
  syncOptIn: (id: string, optIn: boolean) =>
    call<void>("transcript_sync_opt_in", { id, optIn }),
};

export interface LayoutDto {
  revision: number;
  updatedAt: number;
  data: unknown | null;
}

export interface LayoutSaveResult {
  saved: boolean;
  revision: number;
  conflict: boolean;
}

export const layoutApi = {
  get: () => call<LayoutDto>("layout_get"),
  put: (data: string, revision: number) =>
    call<LayoutSaveResult>("layout_put", { args: { data, revision } }),
};

export interface Asset {
  id: string;
  groupId: string | null;
  kind: "ssh" | "winrm" | "local" | "docker" | "mysql" | "postgres" | "redis";
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
  saveKeyFile: (content: string) =>
    call<{ path: string }>("asset_save_key_file", { content }),
  readKeyFile: (path: string) => call<string>("asset_read_key_file", { path }),
  delete: (id: string) => call<void>("asset_delete", { id }),
  search: (q: string) => call<Asset[]>("asset_search", { q }),
  groupList: () => call<AssetGroup[]>("group_list"),
  groupCreate: (name: string, parentId?: string) =>
    call<AssetGroup>("group_create", { name, parentId }),
  groupUpdate: (id: string, name?: string, parentId?: string | null) =>
    call<AssetGroup>("group_update", { id, name, parentId }),
  groupDelete: (id: string) => call<void>("group_delete", { id }),
  snippetList: () => call<import("./types").SnippetDto[]>("snippet_list"),
  snippetCreate: (name: string, body: string, groupId?: string, sort?: number) =>
    call<{ id: string }>("snippet_create", { name, body, groupId, sort }),
  snippetUpdate: (
    id: string,
    name: string,
    body: string,
    patch: { groupId?: string | null; sort?: number } = {},
  ) => call<void>("snippet_update", { id, name, body, ...patch }),
  snippetDelete: (id: string) => call<void>("snippet_delete", { id }),
  auditQuery: (args: Record<string, unknown> = {}) =>
    call<import("./types").AuditEntryDto[]>("audit_query", { args }),
  auditCount: (args: Record<string, unknown> = {}) =>
    call<import("./types").AuditCountDto>("audit_count", { args }),
  commandQuery: (args: Record<string, unknown> = {}) =>
    call<import("./types").CommandEntryDto[]>("command_query", { args }),
  commandCount: (args: Record<string, unknown> = {}) =>
    call<import("./types").AuditCountDto>("command_count", { args }),
  probeBatch: (assetIds: string[], timeoutMs?: number, maxConcurrent?: number) =>
    call<import("./types").AssetProbeBatchDto>("asset_probe_batch", {
      args: { assetIds, timeoutMs, maxConcurrent },
    }),
  knownHostList: () => call<import("./types").KnownHostDto[]>("known_host_list"),
  knownHostAccept: (host: string, port: number, keyType: string, fingerprint: string) =>
    call<void>("known_host_accept", { host, port, keyType, fingerprint }),
  knownHostRemove: (id: string) => call<void>("known_host_remove", { id }),
  appInfo: () =>
    call<{ name: string; version: string; vault: import("./types").VaultStatusDto }>(
      "app_info",
    ),
};

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
  readRange: (sessionId: string, path: string, offset: number, maxBytes?: number) =>
    call<{
      path: string;
      offset: number;
      size: number;
      contentBase64: string;
      truncated: boolean;
    }>("fs_read_range", {
      sessionId,
      path,
      offset,
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
  packDownload: (sessionId: string, remotePath: string, localPath: string) =>
    call<number>("fs_pack_download", { sessionId, remotePath, localPath }),
  extract: (sessionId: string, path: string) =>
    call<string>("fs_extract", { sessionId, path }),
};

export const mountApi = {
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

export interface FilesSettingsView {
  publicBaseURL: string;
}

export interface SavedImageView {
  path: string;
  bytes: number;
}

export const filesApi = {
  settingsGet: () => call<FilesSettingsView>("files_settings_get"),
  settingsSet: (publicBaseURL: string) =>
    call<FilesSettingsView>("files_settings_set", { publicBaseURL }),
  saveImage: (path: string, contentBase64: string) =>
    call<SavedImageView>("files_save_image", { path, contentBase64 }),
};

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
  ) =>
    call<string>("docker_logs_attach", {
      sessionId,
      containerId,
      tail,
      channel,
      clientId: clientId(),
    }),
  execAttach: (
    sessionId: string,
    containerId: string,
    cols: number,
    rows: number,
    channel: unknown,
    cmd?: string,
  ) =>
    call<string>("docker_exec_attach", {
      args: { sessionId, containerId, cmd, cols, rows, clientId: clientId() },
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

export interface QueryResult {
  columns: string[];
  rows: unknown[][];
  rowsAffected: number;
  durationMs: number;
  truncated: boolean;
  error: string | null;
}

export interface TableColumn {
  name: string;
  type: string;
  nullable: boolean;
  key: string;
  default: string | null;
  extra: string;
}

export interface TableIndex {
  name: string;
  unique: boolean;
  column: string;
  seq: number;
}

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

export interface ProviderConfig {
  baseUrl: string;
  apiKey: string;
  model: string;
  temperature: number;
  contextWindow: number;
  proxy: string | null;
  stream: boolean;
}

export type AiPermissionMode = "read_only" | "read_write" | "silent";

export interface AiPermissionConfig {
  mode: AiPermissionMode;
  dangerRules: string[];
}

export type AiDecision = "allow" | "allow_session" | "allow_persistent" | "deny";

export interface AiConfirmationInput {
  jobId: string;
  callId: string;
  nonce: string;
  decision: AiDecision;
}

export interface AiAnswerInput {
  jobId: string;
  callId: string;
  nonce: string;
  text: string;
}

export interface TakeoverRunResult {
  jobId: string;
  token: string;
}

export const aiApi = {
  chat: (
    args: {
      conversationId?: string;
      scope: Record<string, unknown>;
      message: string;
      selection?: string;
      images?: string[];
      planMode?: boolean;
      channel: unknown;
    },
  ) => {
    const { channel, ...body } = args;
    return call<{ jobId: string; conversationId: string }>("ai_chat", { args: body, channel });
  },
  getPermission: () => call<AiPermissionConfig>("ai_get_permission"),
  setPermission: (config: AiPermissionConfig) =>
    call<void>("ai_set_permission", { config }),
  cancel: (jobId: string) => call<void>("ai_cancel", { jobId }),
  steer: (jobId: string, message: string) => call<void>("ai_steer", { jobId, message }),
  editResend: (conversationId: string, messageId: string) =>
    call<void>("ai_edit_resend", { conversationId, messageId }),
  confirm: (input: AiConfirmationInput, channel?: unknown) =>
    call<void>("ai_confirm", { ...input, ...(channel === undefined ? {} : { channel }) }),
  answer: (input: AiAnswerInput, channel?: unknown) =>
    call<void>("ai_answer", { ...input, ...(channel === undefined ? {} : { channel }) }),
  hitlSnapshot: (jobId: string) =>
    call<import("./types").AiHitlSnapshotDto>("ai_hitl_snapshot", { jobId }),
  hitlEvents: (jobId: string, afterSeq: number) =>
    call<import("./types").AiHitlEventDto[]>("ai_hitl_events", { jobId, afterSeq }),
  runs: (conversationId: string, limit?: number) =>
    call<import("./types").AiRunDto[]>("ai_run_list", { conversationId, limit }),
  runEvents: (jobId: string, afterSeq: number, limit?: number) =>
    call<import("./types").AiRunEventDto[]>("ai_run_events", { jobId, afterSeq, limit }),
  models: () => call<string[]>("ai_models"),
  testProvider: () => call<import("./types").ProviderTestResult>("ai_test_provider"),
  setProvider: (config: ProviderConfig) =>
    call<void>("ai_set_provider", { config }),
  getProvider: () => call<ProviderConfig>("ai_get_provider"),
  presets: () => call<string[]>("ai_presets"),
  takeoverEnter: (tabId: string) => call<{ token: string }>("ai_takeover_enter", { tabId }),
  takeoverExit: (tabId: string, token: string, reason?: string) =>
    call<void>("ai_takeover_exit", { tabId, token, reason }),
  takeoverRun: (args: {
    tabId: string;
    token: string;
    instruction: string;
    allowWrite: boolean;
    channel: unknown;
  }): Promise<TakeoverRunResult> => {
    const { channel, ...body } = args;
    return call<TakeoverRunResult>("ai_takeover_run", { ...body, channel });
  },
  conversationList: () =>
    call<import("./types").ConversationDto[]>("ai_conversation_list"),
  conversationDelete: (id: string) => call<void>("ai_conversation_delete", { id }),
  conversationCreate: (title?: string) =>
    call<import("./types").ConversationDto>("ai_conversation_create", { title }),
  messages: (conversationId: string) =>
    call<import("./types").MessageDto[]>("ai_messages", { conversationId }),
};

import type { AiUsageSummaryRow, ModelProfile, ModelProfilesView, ProviderTestResult } from "./types";
export type { AiUsageSummaryRow, ModelProfile, ModelProfilesView, ProviderTestResult };

export const modelApi = {
  overview: () =>
    call<ModelProfilesView | null>("ai_model_profiles").then(
      (v) => v ?? { profiles: [], activeId: null },
    ),
  save: (profile: ModelProfile) => call<ModelProfile>("ai_model_save", { profile }),
  remove: (id: string) => call<void>("ai_model_delete", { id }),
  activate: (id: string) => call<void>("ai_model_activate", { id }),
  test: (id: string) => call<import("./types").ProviderTestResult>("ai_test_provider", { id }),
  refresh: (profile: ModelProfile) =>
    call<import("./types").ModelListDto>("ai_model_refresh", { profile }),
  presets: () => call<string[] | null>("ai_presets").then((v) => v ?? []),
  preset: (name: string) => call<ModelProfile>("ai_model_preset", { preset: name }),
  usageSummary: () =>
    call<AiUsageSummaryRow[] | null>("ai_usage_summary").then((v) => v ?? []),
  circuitStatus: (id: string) =>
    call<import("./types").AiCircuitStatusDto>("ai_circuit_status", { id }),
};

export interface VaultStatus {
  initialized: boolean;
  mode: "not_init" | "dpapi" | "master";
  unlocked: boolean;
  autoLockMinutes: number;
}

export interface CredentialUsedBy {
  id: string;
  name: string;
  kind: string;
}

export type CredentialSource = "inline" | "file";

export interface Credential {
  id: string;
  name: string;
  kind: string;
  createdAt: number;
  updatedAt: number;
  usedBy: CredentialUsedBy[];
  source: CredentialSource | null;
  refPath: string | null;
  hasPassphrase: boolean;
}

export interface RevealedCredential {
  kind: string;
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
  setAutoLock: (minutes: number) => call<void>("vault_set_autolock", { minutes }),
  changePassword: (oldPassword: string, newPassword: string) =>
    call<void>("vault_change_password", { oldPassword, newPassword }),
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
  updateCredential: (
    id: string,
    patch: {
      name?: string;
      secret?: string;
      source?: CredentialSource;
      passphrase?: string;
    },
  ) => call<void>("credential_update", { args: { id, ...patch } }),
  generateKey: (name: string, algorithm?: "ed25519" | "rsa", passphrase?: string) =>
    call<import("./types").GeneratedKeyDto>("vault_generate_key", {
      args: { name, algorithm, passphrase },
    }),
};

export type SshImportSource = "ssh-config" | "termius" | "ssh-home";
export type SshImportItemAction = "import" | "overwrite" | "skip";

export const sshImportApi = {
  preview: (args: { source: SshImportSource; path?: string; confirmed?: boolean }) =>
    call<import("./types").SshImportPreviewDto>("ssh_import_preview", { args }),
  apply: (args: {
    source: SshImportSource;
    path?: string;
    confirmed?: boolean;
    hosts: { id: string; action: SshImportItemAction }[];
    keys: { id: string; action: SshImportItemAction }[];
  }) => call<import("./types").SshImportApplyDto>("ssh_import_apply", { args }),
};

export type ForwardSpec = import("./types").ForwardSpecDto & { listenHost?: string };

export const forwardApi = {
  env: () => call<import("./types").ForwardEnvDto>("forward_env"),
  create: (sessionId: string, listenPort: number, targetHost: string, targetPort: number) =>
    call<ForwardSpec>("forward_create", {
      sessionId,
      listenPort,
      targetHost,
      targetPort,
    }),
  createSocks: (sessionId: string, listenPort: number, acknowledgeRisk = false) =>
    call<ForwardSpec>("forward_create_socks", {
      sessionId,
      listenPort,
      ...(acknowledgeRisk ? { acknowledgeRisk: true } : {}),
    }),
  createRemote: (
    sessionId: string,
    bindHost: string,
    bindPort: number,
    targetHost: string,
    targetPort: number,
    acknowledgeRisk = false,
  ) =>
    call<ForwardSpec>("forward_create_remote", {
      sessionId,
      bindHost,
      bindPort,
      targetHost,
      targetPort,
      ...(acknowledgeRisk ? { acknowledgeRisk: true } : {}),
    }),
  list: () => call<ForwardSpec[]>("forward_list"),
  remove: (id: string) => call<void>("forward_remove", { id }),
};

export const syncApi = {
  digest: () => call<import("./types").SyncDigest>("sync_digest"),

  exportAssets: (assetIds: string[], withCreds: boolean) =>
    call<import("./types").SyncBundle>("sync_export", { args: { assetIds, withCreds } }),

  importBundle: (bundle: import("./types").SyncBundle, force: boolean) =>
    call<import("./types").ImportReport>("sync_import", { args: { bundle, force } }),

  readBundleFile: (path: string, password?: string) =>
    call<string>("sync_bundle_read", { args: { path, password } }),

  writeBundleFile: (path: string, content: string, password?: string) =>
    call<import("./types").SyncBundleWriteResult>("sync_bundle_write", {
      args: { path, content, password },
    }),

  linkGet: () => call<import("./types").AccountLink>("sync_link_get"),
  linkSet: (patch: { url?: string; username?: string; password?: string; insecure?: boolean }) =>
    call<import("./types").AccountLink>("sync_link_set", { args: patch }),

  status: () => call<import("./types").SyncStatus>("sync_status"),
  syncNow: () => call<import("./types").SyncReport>("sync_now"),

  applyObjects: (objects: import("./types").SyncApplyObject[]) =>
    call<import("./types").SyncApplyResult>("sync_apply_objects", { args: { objects } }),

  collectAssets: (includeDeleted: boolean, afterId?: string, limit?: number) =>
    call<import("./types").SyncCollectAssetsResult>("sync_collect_assets", {
      args: { includeDeleted, afterId, limit },
    }),

  collectTombstones: (afterId?: string, limit?: number) =>
    call<import("./types").SyncCollectTombstonesResult>("sync_collect_tombstones", {
      args: { afterId, limit },
    }),

  collectCredentials: (revealSecrets: boolean, afterId?: string, limit?: number) =>
    call<import("./types").SyncCollectCredentialsResult>("sync_collect_credentials", {
      args: { revealSecrets, afterId, limit },
    }),

  collectKnownHosts: (afterId?: string, limit?: number) =>
    call<import("./types").SyncCollectKnownHostsResult>("sync_collect_known_hosts", {
      args: { afterId, limit },
    }),

  collectAIProfiles: (revealSecrets: boolean, afterId?: string, limit?: number) =>
    call<import("./types").SyncCollectAIProfilesResult>("sync_collect_ai_profiles", {
      args: { revealSecrets, afterId, limit },
    }),

  kindOptInGet: () => call<import("./types").SyncKindOptIn>("sync_kind_opt_in_get"),

  kindOptInSet: (patch: { knownHost?: boolean, aiProfile?: boolean }) =>
    call<import("./types").SyncKindOptIn>("sync_kind_opt_in_set", { args: patch }),
};
