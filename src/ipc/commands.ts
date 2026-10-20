import { WEB } from "./env";
import { clientId, httpUrl } from "./env";
import { describeError } from "../ui/errorText";
import { authedFetch } from "./serverAuth";
import { callDesktop } from "./wails";
import { currentAuthSessionEpoch } from "./authSessionEpoch";
import { notifyAssetDataChanged } from "./assetEvents";
import type {
  ipcContractManifest,
  IPCCommandName,
  IPCInput,
  IPCLegacyCommandName,
  IPCOutput,
  IPCTypedCommandName,
} from "./generated/manifest";

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

type IPCCommandParameter<Command> = 0 extends 1 & Command ? never : Command;
type IPCEnvelopeArgs<Command extends IPCCommandName> =
  (typeof ipcContractManifest.commands)[Command]["channel"] extends true
    ? { channel: unknown; clientId?: string }
    : { channel?: unknown; clientId?: string };
type IPCCallArgs<Command extends IPCTypedCommandName> =
  (typeof ipcContractManifest.commands)[Command]["nested"] extends true
    ? { args: IPCInput<Command> } & IPCEnvelopeArgs<Command>
    : IPCInput<Command> extends Record<string, never>
      ? IPCEnvelopeArgs<Command>
      : IPCInput<Command> & IPCEnvelopeArgs<Command>;
type IPCCallParameters<Command extends IPCTypedCommandName> =
  IPCInput<Command> extends Record<string, never>
    ? (typeof ipcContractManifest.commands)[Command]["channel"] extends true
      ? [args: IPCCallArgs<Command>]
      : [args?: IPCCallArgs<Command>]
    : [args: IPCCallArgs<Command>];
type IPCLegacyEnvelopeArgs = { channel?: unknown; clientId?: string };
type IPCLegacyCallArgs<Command extends IPCLegacyCommandName> =
  (typeof ipcContractManifest.commands)[Command]["nested"] extends true
    ? { args: unknown } & IPCLegacyEnvelopeArgs
    : Record<string, unknown> & IPCLegacyEnvelopeArgs;
type IPCLegacyCallParameters<Command extends IPCLegacyCommandName> =
  (typeof ipcContractManifest.commands)[Command]["nested"] extends true
    ? [args: IPCLegacyCallArgs<Command>]
    : [args?: IPCLegacyCallArgs<Command>];
type IPCLegacyInvocation<Command extends IPCLegacyCommandName> = Command extends unknown
  ? [cmd: Command, ...args: IPCLegacyCallParameters<Command>]
  : never;

export function call<Command extends IPCTypedCommandName>(
  cmd: IPCCommandParameter<Command>,
  ...args: IPCCallParameters<Command>
): Promise<IPCOutput<Command>>;
export function call<T>(...args: IPCLegacyInvocation<IPCLegacyCommandName>): Promise<T>;
export function call<T>(cmd: IPCCommandName, args?: Record<string, unknown>): Promise<T> {
  return invoke<T>(cmd, args);
}

export function unsafeCall<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  return invoke<T>(cmd, args);
}

async function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
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

// With auth=on, the server enforces the CSRF header on every POST of a cookie session (including /rpc read commands);
// the rejection shape is always 403 + { code:"forbidden", message contains "CSRF" }.
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

async function webTransport<T>(operation: Promise<T>): Promise<T> {
  try {
    return await operation;
  } catch (e) {
    if (e instanceof TypeError) {
      throw { code: "disconnected", message: describeError(e) } satisfies AppError;
    }
    throw e;
  }
}

function postRpc(payload: string, csrf: string | null): Promise<Response> {
  return webTransport(
    authedFetch(httpUrl("/rpc"), {
      method: "POST",
      headers: {
        "content-type": "application/json",
        ...(csrf ? { [CSRF_HEADER]: csrf } : {}),
      },
      body: payload,
    }),
  );
}

// On cold start, before the csrf_token is fetched via /auth/me, concurrent first /rpc calls would all 403 if sent bare and then each recover.
// When the token is missing, share one me() first: all waiters share the same promise, so me runs at most once concurrently;
// success or failure is tried only this once; failure (401 not logged in / auth=off always fails) is covered by the 403 retry path below,
// avoiding repeated prefetch for every /rpc in sessionless deployments. Skip this branch when the token already exists.
let csrfBootstrap: Promise<unknown> | null = null;

function bootstrapCsrfToken(): Promise<unknown> {
  if (!csrfBootstrap) {
    // Dynamically load authApi like callWeb does, to avoid a static cycle.
    csrfBootstrap = import("./authApi").then(({ authApi }) => authApi.me().catch(() => null));
  }
  return csrfBootstrap;
}

async function callWeb<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const startedEpoch = currentAuthSessionEpoch();
  const ensureCurrent = () => {
    if (currentAuthSessionEpoch() !== startedEpoch) {
      throw { code: "account_changed", message: "账号已切换，旧请求已取消" } satisfies AppError;
    }
  };
  const { channel, clientId: stableClientId, ...requestBody } = args ?? {};
  const requestPayload = JSON.stringify({
    cmd,
    args: args ? requestBody : null,
    ...(channel === undefined ? {} : { channel }),
    ...(stableClientId === undefined ? {} : { clientId: stableClientId }),
  });
  // Lazily load authApi: commands and authApi reference each other; dynamic import avoids a static cycle.
  const { authApi, getCsrfToken } = await import("./authApi");
  if (getCsrfToken() === null) {
    await bootstrapCsrfToken();
  }
  ensureCurrent();
  let res = await postRpc(requestPayload, getCsrfToken());
  ensureCurrent();
  let text = await webTransport(res.text());
  ensureCurrent();
  if (isCsrfRejection(res.status, text)) {
    // The session may have rotated and invalidated the old token: refresh via /auth/me and retry the same request, only once.
    // CSRF rejection happens in middleware before the command handler, so retrying has no duplicate side effects.
    await authApi.me();
    ensureCurrent();
    res = await postRpc(requestPayload, getCsrfToken());
    ensureCurrent();
    text = await webTransport(res.text());
    ensureCurrent();
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
  insets: () => call<{ top: number; right: number; bottom: number; left: number }>("system_insets"),
};

export interface DesktopTrayDto {
  enabled: boolean;
}

// Desktop tray preference (desktop builds only; the server assembly answers
// CodeUnsupported): enabled=false means closing the window quits the app.
export const desktopTrayApi = {
  get: () => call<DesktopTrayDto>("desktop_tray_get"),
  set: (enabled: boolean) => call<DesktopTrayDto>("desktop_tray_set", { enabled }),
};

export interface BiometricStatus {
  available: boolean;
  canEvaluate: boolean;
  hasMaterial: boolean;
}

export interface BiometricUnlockResult {
  ok: boolean;
  password?: string;
  message?: string;
}

// Biometric quick unlock (iOS/Android): the unlock material is the credential vault master password; iOS stores it in the Keychain
// (access control bound to the current Face ID set), Android in Keystore-encrypted EncryptedSharedPreferences;
// verification happens in the system biometric prompt; on other platforms status.available is always false.
export const biometricApi = {
  status: () => call<BiometricStatus>("biometric_status"),
  unlock: () => call<BiometricUnlockResult>("biometric_unlock"),
  enable: (password: string) => call<void>("biometric_enable", { password }),
  disable: () => call<void>("biometric_disable"),
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
  connect: (assetId: string) =>
    call<SessionInfo>("session_connect", { args: { assetId } }),
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
  connectDevice: (deviceId: string, name: string) =>
    call<SessionInfo>("session_connect_device", { args: { deviceId, name } }),
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
    call<AttachedTabInfo>("session_open_line_tab", {
      sessionId,
      cols,
      rows,
      channel,
      clientId: clientId(),
    }),
  lineExec: (tabId: string, line: string) =>
    call<void>("session_line_exec", { tabId, line, clientId: clientId() }),
};

export type TerminalShell = "powershell" | "cmd" | "posix" | "unknown";

export interface AttachedTabInfo {
  tabId: string;
  sessionId: string;
  cols: number;
  rows: number;
  controller: string | null;
  subscribers: number;
  viewers: number;
  exited: boolean;
  retainable?: boolean;
  ephemeral?: boolean;
  encoding?: string;
  title?: string;
  shell?: TerminalShell;
  fallbackNotice?: string;
  attachmentId?: number;
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
  lastOutputAt?: number;
  lastOutputMsAgo: number;
  title?: string;
}

export type RecordingState = "never-started" | "running" | "stopped" | "failed";

export interface RecordingStatus {
  tabId: string;
  state: RecordingState;
  bytes: number;
  path?: string;
  safeError?: string;
  generation: number;
}

export interface DurableDescriptor {
  id: string;
  incarnation: string;
  createdAt: number;
  title?: string;
  metadataRevision: number;
  cols: number;
  rows: number;
  dead: boolean;
  exitCode: number | null;
  signal?: string;
  finishedAt: number | null;
}

export interface StreamResumeAnchor {
  seq: number;
  epoch: string;
}

function listTerminal<T>(sessionId?: string, durable?: boolean): Promise<T> {
  const args =
    sessionId === undefined && durable === undefined
      ? undefined
      : {
          ...(sessionId === undefined ? {} : { sessionId }),
          ...(durable === undefined ? {} : { durable }),
        };
  return call<T>("terminal_list", args);
}

export interface BroadcastProductionConfirmation {
  confirmed: boolean;
  targetSetHash: string;
  inputHash: string;
}

export interface TerminalBroadcastWriteOptions {
  targetTabIds: string[];
  productionConfirmation?: BroadcastProductionConfirmation;
}

export interface BroadcastConfirmationTarget {
  tabId: string;
  sessionId: string;
  sessionName: string;
  assetId?: string;
  hostname?: string;
  environment: string;
}

export interface BroadcastConfirmationChallenge {
  kind: "broadcast_production_confirmation";
  targetSetHash: string;
  inputHash: string;
  productionTargets: BroadcastConfirmationTarget[];
}

export const terminalApi = {
  attach: (sessionId: string, cols: number, rows: number, channel: unknown, durable?: boolean) =>
    call<AttachedTabInfo>("terminal_attach", {
      sessionId,
      cols,
      rows,
      channel,
      clientId: clientId(),
      ...(durable === undefined ? {} : { durable }),
    }),
  attachTab: (tabId: string, channel: unknown, replayBytes?: number, resume?: StreamResumeAnchor, sessionId?: string) =>
    call<AttachedTabInfo>("terminal_attach_tab", {
      tabId,
      replayBytes,
      channel,
      clientId: clientId(),
      ...(resume ? { fromSeq: resume.seq, fromEpoch: resume.epoch } : {}),
      ...(sessionId === undefined ? {} : { sessionId }),
    }),
  prepareBroadcast: (
    tabIds: readonly string[],
    data: Uint8Array,
    productionConfirmation?: BroadcastProductionConfirmation,
  ) =>
    call<void>("terminal_broadcast_prepare", {
      args: {
        tabIds: [...tabIds],
        data: Array.from(data),
        ...(productionConfirmation ? { productionConfirmation } : {}),
      },
    }),
  write: (tabId: string, data: Uint8Array, broadcast?: TerminalBroadcastWriteOptions) =>
    call<void>("terminal_write", {
      args: {
        tabId,
        data: Array.from(data),
        clientId: clientId(),
        ...(broadcast ? { broadcast } : {}),
      },
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
  listLive: () => listTerminal<LiveTabInfo[]>(),
  listDurable: (sessionId: string) => listTerminal<DurableDescriptor[]>(sessionId, true),
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
  recordingStatus: (tabId: string) =>
    call<RecordingStatus>("terminal_record_status", { tabId }),
  exportLog: (tabId: string, path: string, maxBytes?: number) =>
    call<number>("terminal_export_log", { tabId, path, maxBytes }),
  closeTab: (tabId: string, mode?: "kill" | "detach", sessionId?: string) =>
    call<void>("terminal_close_tab", { tabId, mode, clientId: clientId(), ...(sessionId === undefined ? {} : { sessionId }) }),
  renameTab: (tabId: string, title: string) =>
    call<AttachedTabInfo>("terminal_rename_tab", { tabId, title }),
  createTab: (sessionId: string, options?: { title?: string; command?: string }) =>
    call<AttachedTabInfo>("terminal_create_tab", {
      sessionId,
      title: options?.title,
      command: options?.command,
    }),
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
  storedBytes?: number;
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
  kind: "ssh" | "winrm" | "local" | "docker" | "docker-context" | "kubernetes" | "mysql" | "postgres" | "redis";
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

export interface AssetSearchResponse {
  assets: Asset[];
  total: number;
  truncated: boolean;
}

async function assetWrite<T>(operation: () => Promise<T>): Promise<T> {
  const result = await operation();
  notifyAssetDataChanged();
  return result;
}

export const assetApi = {
  list: () => call<Asset[]>("asset_list"),
  get: (id: string) => call<Asset>("asset_get", { id }),
  create: (args: Partial<Asset> & { kind: string; name: string }) =>
    assetWrite(() => call<Asset>("asset_create", { args })),
  update: (args: { id: string } & Record<string, unknown>) =>
    assetWrite(() => call<Asset>("asset_update", { args })),
  readKeyFile: (path: string) => call<string>("asset_read_key_file", { path }),
  delete: (id: string) => assetWrite(() => call<void>("asset_delete", { id })),
  search: (q: string) => call<Asset[]>("asset_search", { q }),
  searchMeta: (q: string) => call<AssetSearchResponse>("asset_search_meta", { q }),
  groupList: () => call<AssetGroup[]>("group_list"),
  groupCreate: (name: string, parentId?: string) =>
    assetWrite(() => call<AssetGroup>("group_create", { name, parentId })),
  groupUpdate: (id: string, name?: string, parentId?: string | null) =>
    assetWrite(() => call<AssetGroup>("group_update", { id, name, parentId })),
  groupDelete: (id: string) => assetWrite(() => call<void>("group_delete", { id })),
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
  knownHostAccept: (host: string, port: number, keyType: string, fingerprint: string, key?: string) =>
    call<void>("known_host_accept", { host, port, keyType, fingerprint, key }),
  knownHostRemove: (id: string) => call<void>("known_host_remove", { id }),
};

function normEntryPath<T extends { path: string }>(e: T): T {
  return { ...e, path: e.path.replace(/\\/g, "/") };
}

export interface FileRootDto {
  path: string;
  label: string;
}

export interface FileRootsDto {
  pathStyle: "windows" | "posix" | "unknown";
  roots: FileRootDto[];
  homePath: string | null;
  supported: boolean;
}

export const fsApi = {
  roots: async (sessionId: string) => {
    const result = await call<FileRootsDto>("fs_roots", { sessionId });
    return {
      ...result,
      roots: result.roots.map(normEntryPath),
      homePath: result.homePath?.replace(/\\/g, "/") ?? null,
    };
  },
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
    taskId?: string,
    attemptId?: string,
  ) => call<void>("fs_write", { args: { sessionId, path, contentBase64, backup, taskId, attemptId } }),
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
  upload: (
    sessionId: string,
    localPath: string,
    remotePath: string,
    resume?: boolean,
    taskId?: string,
    attemptId?: string,
  ) => call<number>("fs_upload", { sessionId, localPath, remotePath, resume, taskId, attemptId }),
  uploadBegin: (args: {
    sessionId: string;
    remotePath: string;
    totalSize: number;
    chunkSize?: number;
    taskId: string;
    attemptId: string;
  }) => call<{ uploadId: string; chunkSize: number }>("fs_upload_begin", args),
  uploadChunk: (args: {
    sessionId: string;
    uploadId: string;
    taskId: string;
    attemptId: string;
    index: number;
    size: number;
    sha256: string;
    contentBase64: string;
  }) => call<{ received: number; total: number }>("fs_upload_chunk", args),
  uploadCommit: (args: { sessionId: string; uploadId: string; taskId: string; attemptId: string }) =>
    call<number>("fs_upload_commit", args),
  uploadAbort: (args: { sessionId: string; uploadId: string; taskId: string; attemptId: string }) =>
    call<void>("fs_upload_abort", args),
  download: (
    sessionId: string,
    remotePath: string,
    localPath: string,
    resume?: boolean,
    taskId?: string,
    attemptId?: string,
  ) => call<number>("fs_download", { sessionId, remotePath, localPath, resume, taskId, attemptId }),
  packDownload: (sessionId: string, remotePath: string, localPath: string, taskId?: string, attemptId?: string) =>
    call<number>("fs_pack_download", { sessionId, remotePath, localPath, taskId, attemptId }),
  cancel: (taskId: string, attemptId?: string) => call("fs_cancel", { taskId, attemptId }),
  extract: (sessionId: string, path: string) =>
    call<string>("fs_extract", { sessionId, path }),
};

export interface LocalFolderFileDto {
  relativePath: string;
  localPath: string;
  size: number;
  mtimeMs: number;
}

export interface LocalFolderListDto {
  name: string;
  files: LocalFolderFileDto[];
  emptyDirs: string[];
}

// Desktop-only commands (not registered in the server assembly): enumerate and read a local
// folder picked via the native folder dialog, giving folder upload real OS paths.
export const localFileApi = {
  listFolder: (path: string) => call<LocalFolderListDto>("local_folder_list", { path }),
  read: (path: string, maxBytes?: number) =>
    call<{ path: string; size: number; contentBase64: string }>("local_file_read", {
      path,
      maxBytes,
    }),
  readRange: (path: string, offset: number, maxBytes?: number) =>
    call<{
      path: string;
      offset: number;
      size: number;
      contentBase64: string;
      truncated: boolean;
    }>("local_file_read_range", { path, offset, maxBytes }),
};

export const mountApi = {
  capability: () => call<string | null>("mount_capability"),
  list: (options: { sessionId: string; forceRefresh?: boolean }) =>
    call<import("./types").MountEntryDto[]>("mount_list", options),
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

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  composeProject: string | null;
  composeService?: string | null;
}

export interface ImageSummary {
  id: string;
  repository: string;
  tag: string;
  size: string;
  createdSince: string;
}

export interface ComposeStack {
  project: string;
  workingDir: string;
  configFiles: string[];
  envPath: string;
  envExists: boolean;
  services: string[];
  readOnly: boolean;
  reason?: string;
}

export interface ComposeOverview {
  cli?: "docker compose" | "docker-compose";
  available: boolean;
  message?: string;
  stacks: ComposeStack[];
}

export type DockerProductionConfirmation = {
  confirmed: true;
  targetSetHash: string;
  commandHash: string;
};

export interface DiskCategory {
  kind: "images" | "containers" | "volumes" | "buildCache";
  total: number;
  active: number;
  sizeBytes: number;
  reclaimableBytes: number;
}

export interface DockerContainerUse {
  id: string;
  name: string;
}

export interface DockerDiskUsage {
  categories: DiskCategory[];
  images: {
    id: string;
    repository: string;
    tag: string;
    containers: number;
    sizeBytes: number;
    uniqueSizeBytes: number;
  }[];
  containers: { id: string; name: string; image: string; status: string; sizeBytes: number }[];
  volumes: {
    name: string;
    driver: string;
    links: number;
    sizeBytes: number;
    containers: DockerContainerUse[];
  }[];
  networks: {
    id: string;
    name: string;
    driver: string;
    scope: string;
    builtin: boolean;
    containers: DockerContainerUse[];
  }[];
  buildCache: { id: string; type: string; sizeBytes: number; shared: boolean }[];
}

export type DockerPruneKind = "containers" | "images" | "volumes" | "networks" | "buildCache";
export interface DockerPruneItem {
  kind: DockerPruneKind;
  id: string;
  name: string;
  bytes: number;
}
export interface DockerPrunePreview {
  token: string;
  digest: string;
  expiresAt: number;
  items: DockerPruneItem[];
  totalBytes: number;
}
export interface DockerPruneResult {
  items: { item: DockerPruneItem; output?: string; error?: string }[];
  freedBytes: number;
}

export type ComposeRedeployPrepareRequest = {
  sessionId: string;
  project: string;
  path: string;
  baseContentBase64: string;
  contentBase64: string;
  productionConfirmation?: DockerProductionConfirmation;
};

export type ComposeRedeployAttachRequest = {
  sessionId: string;
  project: string;
  path: string;
  ticket: string;
  channel: unknown;
};

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
  composeStacks: (sessionId: string) => call<ComposeOverview>("docker_compose_stacks", { sessionId }),
  composeAction: (
    sessionId: string,
    project: string,
    action: "up" | "down" | "restart" | "pull",
    productionConfirmation?: DockerProductionConfirmation,
  ) =>
    call<string>("docker_compose_action", {
      sessionId,
      project,
      action,
      ...(productionConfirmation ? { productionConfirmation } : {}),
    }),
  composeLogsAttach: (sessionId: string, project: string, tail: number, channel: unknown) =>
    call<string>("docker_compose_logs_attach", { sessionId, project, tail, channel, clientId: clientId() }),
  composeRedeployPrepare: (request: ComposeRedeployPrepareRequest) =>
    call<{ ticket: string; contentHash: string }>("docker_compose_redeploy_prepare", request),
  composeRedeployAttach: (request: ComposeRedeployAttachRequest) => {
    const { channel, ...body } = request;
    return call<string>("docker_compose_redeploy_attach", { ...body, channel, clientId: clientId() });
  },
  diskUsage: (sessionId: string) => call<DockerDiskUsage>("docker_disk_usage", { sessionId }),
  prunePreview: (sessionId: string, kinds: DockerPruneKind[]) =>
    call<DockerPrunePreview>("docker_prune_preview", { sessionId, kinds }),
  prune: (
    sessionId: string,
    token: string,
    digest: string,
    productionConfirmation?: DockerProductionConfirmation,
  ) =>
    call<DockerPruneResult>("docker_prune", {
      sessionId,
      token,
      digest,
      ...(productionConfirmation ? { productionConfirmation } : {}),
    }),
  reclaimable: (sessionId: string) =>
    call<{ totalBytes: number; categories: DiskCategory[] }>("docker_reclaimable", { sessionId }),
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
  connect: (assetId: string) =>
    call<{ connId: string }>("db_connect", { args: { assetId } }),
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
  temperature?: number;
  reasoningEffort?: string;
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
      refs?: { kind: string; id: string; label: string; path?: string; sessionId?: string }[];
      planMode?: boolean;
      channel: unknown;
    },
  ) => {
    const { channel, ...body } = args;
    return call<{ jobId: string; conversationId: string }>("ai_chat", { args: body, channel });
  },
  getPermission: (scope?: Record<string, unknown>) =>
    call<AiPermissionConfig>("ai_get_permission", scope ? { scope } : {}),
  setPermission: (config: AiPermissionConfig, scope?: Record<string, unknown>, followGlobal?: boolean) =>
    call<void>("ai_set_permission", {
      config,
      ...(scope ? { scope } : {}),
      ...(followGlobal ? { followGlobal: true } : {}),
    }),
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
  run: (jobId: string) =>
    call<import("./types").AiRunDto>("ai_run_get", { jobId }),
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
  takeoverConfirm: (input: AiConfirmationInput) => call<void>("ai_takeover_confirm", { ...input }),
  takeoverCancel: (jobId: string) => call<void>("ai_takeover_cancel", { jobId }),
  takeoverPause: (tabId: string, token: string) =>
    call<void>("ai_takeover_pause", { tabId, token }),
  takeoverResume: (args: {
    tabId: string;
    token: string;
    jobId: string;
    channel: unknown;
  }): Promise<TakeoverRunResult> => {
    const { channel, ...body } = args;
    return call<TakeoverRunResult>("ai_takeover_resume", { ...body, channel });
  },
  conversationList: () =>
    call<import("./types").ConversationDto[]>("ai_conversation_list"),
  conversationDelete: (id: string) => call<void>("ai_conversation_delete", { id }),
  conversationRetitle: (conversationId: string) =>
    call<string>("ai_conversation_retitle", { conversationId }),
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
  passwordless: boolean;
  systemProtection: boolean;
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
  initDpapi: (password = "") => call<void>("vault_init_dpapi", { password }),
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
  retry: (id: string, sessionId?: string) => call<ForwardSpec>("forward_retry", {
    id,
    ...(sessionId ? { sessionId } : {}),
  }),
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
  wake: () => call<void>("sync_wake"),

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

  collectAIPermission: () => call<import("./types").SyncCollectAIPermission>("sync_collect_ai_permission"),

  collectAIGrants: () => call<import("./types").SyncCollectAIGrants>("sync_collect_ai_grants"),

  collectPreferences: () => call<import("./types").SyncCollectPreference[]>("sync_collect_preferences"),

  preferencesGet: () => call<import("./types").PreferenceScopeView>("sync_preferences_get"),

  preferencesUpdate: (update: { set?: Record<string, unknown>; clear?: string[] }) =>
    call<import("./types").PreferenceScopeView>("sync_preferences_set", { args: update }),

  kindOptInGet: () => call<import("./types").SyncKindOptIn>("sync_kind_opt_in_get"),

  kindOptInSet: (patch: Partial<import("./types").SyncKindOptIn>) =>
    call<import("./types").SyncKindOptIn>("sync_kind_opt_in_set", { args: patch }),
};
