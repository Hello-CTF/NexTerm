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

// SYNCED_WRITES change data the account sync carries. After one succeeds the
// desktop wakes a sync round, so the edit reaches other devices in moments
// instead of on the next periodic round.
const SYNCED_WRITES = new Set([
  "asset_create", "asset_update", "asset_delete",
  "group_create", "group_update", "group_delete",
  "snippet_create", "snippet_update", "snippet_delete",
  "credential_update", "vault_set_credential", "vault_delete_credential",
  "known_host_accept", "known_host_remove",
  "ai_set_permission", "ai_model_save", "ai_model_delete", "ai_model_activate",
  "ai_grant_set", "ai_grant_revoke", "ai_grant_rule_set", "ai_grant_rule_revoke",
  "sync_preferences_set", "inventory_sync", "transcript_sync_opt_in", "transcript_delete",
]);
export const SYNC_WAKE_DELAY_MS = 2000;
let syncWakeTimer: ReturnType<typeof setTimeout> | null = null;

// scheduleSyncWake debounces bursts such as an import into one sync round.
function scheduleSyncWake(cmd: string): void {
  if (!SYNCED_WRITES.has(cmd)) return;
  if (syncWakeTimer !== null) clearTimeout(syncWakeTimer);
  syncWakeTimer = setTimeout(() => {
    syncWakeTimer = null;
    void callDesktop("sync_wake").catch(() => undefined);
  }, SYNC_WAKE_DELAY_MS);
}

async function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  try {
    if (WEB) {
      return await callWeb<T>(cmd, args);
    }
    const result = await callDesktop<T>(cmd, args);
    scheduleSyncWake(cmd);
    return result;
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
  platform: () => call("app_platform"),
  insets: () => call("system_insets"),
};

export interface DesktopTrayDto {
  enabled: boolean;
}

// Desktop tray preference (desktop builds only; the server assembly answers
// CodeUnsupported): enabled=false means closing the window quits the app.
export const desktopTrayApi = {
  get: (): Promise<DesktopTrayDto> => call("desktop_tray_get"),
  set: (enabled: boolean): Promise<DesktopTrayDto> => call("desktop_tray_set", { enabled }),
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
  status: (): Promise<BiometricStatus> => call("biometric_status"),
  unlock: (): Promise<BiometricUnlockResult> => call("biometric_unlock"),
  enable: (password: string): Promise<void> => call("biometric_enable", { password }) as Promise<void>,
  disable: (): Promise<void> => call("biometric_disable") as Promise<void>,
};

import type { UpdateResultDto, UpdateStatusDto } from "./types";

export const appUpdateApi = {
  check: (): Promise<UpdateStatusDto> => call("app_update_check"),
  install: (version?: string): Promise<UpdateResultDto> =>
    call("app_update_install", { version: version ?? null }),
  restart: (): Promise<UpdateResultDto> => call("app_restart"),
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

function ignoreResult(operation: Promise<unknown>): Promise<void> {
  return operation.then(() => undefined);
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
  quickConnectDefaultUser: () => call("session_quick_connect_user"),
  connectLocal: () => call("session_connect_local") as Promise<SessionInfo>,
  connectDevice: (deviceId: string, name: string) =>
    call("session_connect_device", { args: { deviceId, name } }) as Promise<SessionInfo>,
  disconnect: (sessionId: string) =>
    ignoreResult(call("session_disconnect", { sessionId })),
  reconnect: (sessionId: string) => call("session_reconnect", { sessionId }),
  list: () => call("session_list") as Promise<SessionInfo[]>,
  probe: (host: string, port: number, timeoutMs?: number) =>
    call<{ open: boolean; error?: string }>("session_probe", {
      args: { host, port, timeoutMs },
    }),
  probeHostKey: (assetId: string, timeoutMs?: number) =>
    call<import("./types").SshHostKeyProbeDto>("session_probe_host_key", {
      args: { assetId, timeoutMs },
    }),
  openLineTab: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call("session_open_line_tab", {
      sessionId,
      cols,
      rows,
      channel,
      clientId: clientId(),
    }) as Promise<AttachedTabInfo>,
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

export type HelperCompatibility = "compatible" | "incompatible" | "unknown";
export type HelperOperationAction = "status" | "cancel" | "retry" | "update" | "update_interrupt";

export interface HelperVersionInfo {
  known: boolean;
  release?: string;
  commit?: string;
  protocol?: number | null;
  sha256?: string;
}

export interface HelperUpdateInfo {
  status: "current" | "available" | "deferred" | "interrupted" | "updated" | "rolled_back" | "unknown";
  detail?: string;
  updated?: boolean;
  interrupted?: boolean;
  rolledBack?: boolean;
}

export interface HelperOfflineInfo {
  fileName: string;
  sha256: string;
  protocol?: number | null;
  compatibility: HelperCompatibility;
  trusted: boolean;
}

export interface HelperOperationResult {
  sessionId: string;
  operationId?: string;
  action: string;
  accepted: boolean;
  local: HelperVersionInfo;
  running: HelperVersionInfo;
  compatibility: HelperCompatibility;
  update?: HelperUpdateInfo | null;
  offline?: HelperOfflineInfo | null;
}

function normalizeHelperCompatibility(value: string): HelperCompatibility {
  return value === "compatible" || value === "incompatible" ? value : "unknown";
}

function normalizeHelperUpdateStatus(value: string): HelperUpdateInfo["status"] {
  return value === "current" || value === "available" || value === "deferred" || value === "interrupted" || value === "updated" || value === "rolled_back"
    ? value
    : "unknown";
}

type HelperUpdateInfoWire = Omit<HelperUpdateInfo, "status"> & { status: string };
type HelperOfflineInfoWire = Omit<HelperOfflineInfo, "compatibility"> & { compatibility: string };
type HelperOperationResultWire = Omit<HelperOperationResult, "compatibility" | "update" | "offline"> & {
  compatibility: string;
  update?: HelperUpdateInfoWire | null;
  offline?: HelperOfflineInfoWire | null;
};

function normalizeHelperOperationResult(result: HelperOperationResultWire): HelperOperationResult {
  return {
    ...result,
    compatibility: normalizeHelperCompatibility(result.compatibility),
    update: result.update
      ? { ...result.update, status: normalizeHelperUpdateStatus(result.update.status) }
      : result.update,
    offline: result.offline
      ? { ...result.offline, compatibility: normalizeHelperCompatibility(result.offline.compatibility) }
      : result.offline,
  };
}

export const terminalApi = {
  attach: (sessionId: string, cols: number, rows: number, channel: unknown, durable?: boolean) =>
    call("terminal_attach", {
      sessionId,
      cols,
      rows,
      channel,
      clientId: clientId(),
      ...(durable === undefined ? {} : { durable }),
    }) as Promise<AttachedTabInfo>,
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
    ignoreResult(call("terminal_broadcast_prepare", {
      args: {
        tabIds: [...tabIds],
        data: Array.from(data),
        ...(productionConfirmation ? { productionConfirmation } : {}),
      },
    })),
  write: (tabId: string, data: Uint8Array, broadcast?: TerminalBroadcastWriteOptions) =>
    ignoreResult(call("terminal_write", {
      args: {
        tabId,
        data: Array.from(data),
        clientId: clientId(),
        ...(broadcast ? { broadcast } : {}),
      },
    })),
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
  helperOperation: (sessionId: string, operationId: string, action: HelperOperationAction, interruptActive = false) =>
    call("terminal_helper_operation", { sessionId, operationId, action, interruptActive }).then(normalizeHelperOperationResult),
  helperUpdate: (sessionId: string, interruptActive = false) =>
    call("terminal_helper_operation", { sessionId, action: "update", interruptActive }).then(normalizeHelperOperationResult),
  helperOffline: (sessionId: string, path: string, sha256?: string, confirmed = false) =>
    call("terminal_helper_offline", { sessionId, path, sha256, confirmed }).then(normalizeHelperOperationResult),
  closeTab: (tabId: string, mode?: "kill" | "detach", sessionId?: string) =>
    call<void>("terminal_close_tab", { tabId, mode, clientId: clientId(), ...(sessionId === undefined ? {} : { sessionId }) }),
  renameTab: (tabId: string, title: string) =>
    call("terminal_rename_tab", { tabId, title }) as Promise<AttachedTabInfo>,
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
  hosts: () => call("transcript_hosts") as Promise<TranscriptHost[]>,
  list: async (assetId: string): Promise<TranscriptSummary[]> => {
    const summaries = await call("transcript_list", { assetId });
    return summaries.map((summary) => ({
      id: summary.id,
      sessionId: summary.sessionId,
      assetId: summary.assetId,
      assetName: summary.assetName,
      assetKind: summary.assetKind,
      assetDeleted: summary.assetDeleted,
      startedAt: summary.startedAt,
      endedAt: summary.endedAt,
      bytes: summary.bytes,
      storedBytes: summary.storedBytes,
      chunks: summary.chunks,
      truncated: summary.truncated,
      active: summary.active,
      syncOptIn: summary.syncOptIn,
      contentOmitted: summary.contentOmitted,
    }));
  },
  read: async (id: string, afterSeq = 0, maxBytes?: number): Promise<TranscriptReadResult> => {
    const result = await call("transcript_read", { id, afterSeq, maxBytes });
    return {
      chunks: result.chunks.map((chunk) => ({
        seq: chunk.seq,
        tabId: chunk.tabId,
        ts: chunk.ts,
        kind: chunk.kind,
        dataBase64: chunk.dataBase64,
      })),
      nextSeq: result.nextSeq,
      done: result.done,
      totalBytes: result.totalBytes,
    };
  },
  search: (id: string, query: string) =>
    call("transcript_search", { id, query }) as Promise<TranscriptMatch[]>,
  remove: (id: string) => call("transcript_delete", { id }) as Promise<void>,
  syncOptIn: (id: string, optIn: boolean) =>
    call("transcript_sync_opt_in", { id, optIn }) as unknown as Promise<void>,
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
  get: (): Promise<LayoutDto> => call("layout_get"),
  put: (data: string, revision: number): Promise<LayoutSaveResult> =>
    call("layout_put", { args: { data, revision } }),
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
  list: () => call("asset_list") as Promise<Asset[]>,
  get: (id: string) => call("asset_get", { id }) as Promise<Asset>,
  create: (args: Partial<Asset> & { kind: string; name: string }) =>
    assetWrite(() => call("asset_create", { args }) as Promise<Asset>),
  update: (args: { id: string } & Record<string, unknown>) =>
    assetWrite(() => call("asset_update", {
      args: args as unknown as import("./generated/types").AssetPatchRequest,
    }) as Promise<Asset>),
  readKeyFile: (path: string) => call("asset_read_key_file", { path }),
  delete: (id: string) => assetWrite(() => call("asset_delete", { id }) as Promise<void>),
  search: (q: string) => call("asset_search", { q }) as Promise<Asset[]>,
  searchMeta: (q: string) => call("asset_search_meta", { q }) as Promise<AssetSearchResponse>,
  groupList: () => call("group_list") as Promise<AssetGroup[]>,
  groupCreate: (name: string, parentId?: string) =>
    assetWrite(() => call("group_create", { name, parentId }) as Promise<AssetGroup>),
  groupUpdate: (id: string, name?: string, parentId?: string | null) =>
    assetWrite(() => call("group_update", { id, name, parentId }) as Promise<AssetGroup>),
  groupDelete: (id: string) => assetWrite(() => call("group_delete", { id }) as Promise<void>),
  snippetList: (): Promise<import("./types").SnippetDto[]> => call("snippet_list"),
  snippetCreate: (name: string, body: string, groupId?: string, sort?: number) =>
    call("snippet_create", { name, body, groupId, sort }) as Promise<{ id: string }>,
  snippetUpdate: (
    id: string,
    name: string,
    body: string,
    patch: { groupId?: string | null; sort?: number } = {},
  ) => call("snippet_update", { id, name, body, ...patch }) as Promise<void>,
  snippetDelete: (id: string) => call("snippet_delete", { id }) as Promise<void>,
  auditQuery: (args: Record<string, unknown> = {}) =>
    call("audit_query", { args: args as unknown as IPCInput<"audit_query"> }) as Promise<import("./types").AuditEntryDto[]>,
  auditCount: (args: Record<string, unknown> = {}) =>
    call("audit_count", { args: args as unknown as IPCInput<"audit_count"> }) as Promise<import("./types").AuditCountDto>,
  commandQuery: (args: Record<string, unknown> = {}) =>
    call("command_query", { args: args as unknown as IPCInput<"command_query"> }) as Promise<import("./types").CommandEntryDto[]>,
  commandCount: (args: Record<string, unknown> = {}) =>
    call("command_count", { args: args as unknown as IPCInput<"command_count"> }) as Promise<import("./types").AuditCountDto>,
  probeBatch: (assetIds: string[], timeoutMs?: number, maxConcurrent?: number) =>
    call("asset_probe_batch", {
      args: { assetIds, timeoutMs, maxConcurrent },
    }) as Promise<import("./types").AssetProbeBatchDto>,
  knownHostList: () => call("known_host_list") as Promise<import("./types").KnownHostDto[]>,
  knownHostAccept: (host: string, port: number, keyType: string, fingerprint: string, key?: string) =>
    call("known_host_accept", { host, port, keyType, fingerprint, key }) as Promise<void>,
  knownHostRemove: (id: string) => call("known_host_remove", { id }) as Promise<void>,
};

function normEntryPath<T extends { path: string }>(e: T): T {
  return { ...e, path: e.path.replace(/\\/g, "/") };
}

export type FileRootDto = import("./generated/types").FSRootDTO;

export interface FileRootsDto extends Omit<import("./generated/types").FSRootsDTO, "pathStyle"> {
  pathStyle: "windows" | "posix" | "unknown";
}

export const fsApi = {
  roots: async (sessionId: string) => {
    const result = await call("fs_roots", { sessionId });
    return {
      ...result,
      pathStyle: result.pathStyle as FileRootsDto["pathStyle"],
      roots: result.roots.map(normEntryPath),
      homePath: result.homePath?.replace(/\\/g, "/") ?? null,
    };
  },
  list: async (sessionId: string, path: string) => {
    const list = await call("fs_list", {
      sessionId,
      path,
    });
    return list.map(normEntryPath);
  },
  read: (sessionId: string, path: string, maxBytes?: number) =>
    call("fs_read", {
      sessionId,
      path,
      maxBytes,
    }),
  readRange: (sessionId: string, path: string, offset: number, maxBytes?: number) =>
    call("fs_read_range", {
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
  ) => call("fs_write", { args: { sessionId, path, contentBase64, backup, taskId, attemptId } }) as Promise<void>,
  mkdir: (sessionId: string, path: string) =>
    call("fs_mkdir", { sessionId, path }) as Promise<void>,
  rename: (sessionId: string, from: string, to: string) =>
    call("fs_rename", { sessionId, from, to }) as Promise<void>,
  delete: (sessionId: string, path: string, isDir: boolean) =>
    call("fs_delete", { sessionId, path, isDir }) as Promise<void>,
  chmod: (sessionId: string, path: string, mode: number) =>
    call("fs_chmod", { sessionId, path, mode }) as Promise<void>,
  checksum: (sessionId: string, path: string, algo?: string) =>
    call("fs_checksum", { sessionId, path, algo }),
  upload: (
    sessionId: string,
    localPath: string,
    remotePath: string,
    resume?: boolean,
    taskId?: string,
    attemptId?: string,
  ) => call("fs_upload", { sessionId, localPath, remotePath, resume, taskId, attemptId }),
  uploadBegin: (args: {
    sessionId: string;
    remotePath: string;
    totalSize: number;
    chunkSize?: number;
    taskId: string;
    attemptId: string;
  }) => call("fs_upload_begin", args),
  uploadChunk: (args: {
    sessionId: string;
    uploadId: string;
    taskId: string;
    attemptId: string;
    index: number;
    size: number;
    sha256: string;
    contentBase64: string;
  }) => call("fs_upload_chunk", args),
  uploadCommit: (args: { sessionId: string; uploadId: string; taskId: string; attemptId: string }) =>
    call("fs_upload_commit", args),
  uploadAbort: (args: { sessionId: string; uploadId: string; taskId: string; attemptId: string }) =>
    call("fs_upload_abort", args) as Promise<void>,
  download: (
    sessionId: string,
    remotePath: string,
    localPath: string,
    resume?: boolean,
    taskId?: string,
    attemptId?: string,
  ) => call("fs_download", { sessionId, remotePath, localPath, resume, taskId, attemptId }),
  packDownload: (sessionId: string, remotePath: string, localPath: string, taskId?: string, attemptId?: string) =>
    call("fs_pack_download", { sessionId, remotePath, localPath, taskId, attemptId }),
  cancel: (taskId: string, attemptId?: string) => call("fs_cancel", { taskId, attemptId }),
  extract: (sessionId: string, path: string) =>
    call("fs_extract", { sessionId, path }),
};

export type LocalFolderFileDto = import("./generated/types").LocalFolderFileDTO;
export type LocalFolderListDto = import("./generated/types").LocalFolderListDTO;

// Desktop-only commands (not registered in the server assembly): enumerate and read a local
// folder picked via the native folder dialog, giving folder upload real OS paths.
export const localFileApi = {
  listFolder: (path: string) => call("local_folder_list", { path }),
  read: (path: string, maxBytes?: number) =>
    call("local_file_read", {
      path,
      maxBytes,
    }),
  readRange: (path: string, offset: number, maxBytes?: number) =>
    call("local_file_read_range", { path, offset, maxBytes }),
};

export const mountApi = {
  capability: () => call("mount_capability"),
  list: (options: { sessionId: string; forceRefresh?: boolean }) =>
    call("mount_list", options),
  create: (args: {
    sessionId: string;
    remotePath: string;
    localPoint: string;
    username?: string;
    password?: string;
  }) => call("mount_create", { args }),
  remove: (localPoint: string, sessionId?: string) =>
    call("mount_remove", { localPoint, sessionId }) as Promise<void>,
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

export type DockerProductionConfirmation = Omit<
  import("./generated/types").DockerProductionConfirmation,
  "confirmed"
> & { confirmed: true };

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
    call("docker_overview", { sessionId }) as Promise<{
      containers: ContainerSummary[];
      hostStats: Record<string, number>;
    }>,
  ps: (sessionId: string) => call("docker_ps", { sessionId }) as Promise<ContainerSummary[]>,
  logsAttach: (
    sessionId: string,
    containerId: string,
    tail: number,
    channel: unknown,
  ) =>
    call("docker_logs_attach", {
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
    call("docker_exec_attach", {
      args: { sessionId, containerId, cmd, cols, rows, clientId: clientId() },
      channel,
    }),
  action: (sessionId: string, containerId: string, action: string, newName?: string) =>
    call("docker_action", { args: { sessionId, containerId, action, newName } }) as Promise<void>,
  images: (sessionId: string) => call("docker_images", { sessionId }) as Promise<ImageSummary[]>,
  imagePull: (sessionId: string, image: string) =>
    call("docker_image_pull", { sessionId, image }),
  imageRemove: (sessionId: string, image: string, force?: boolean) =>
    call("docker_image_remove", { sessionId, image, force }) as Promise<void>,
  inspect: (sessionId: string, containerId: string) =>
    call("docker_inspect", { sessionId, containerId }),
  stats: (sessionId: string) => call("docker_stats", { sessionId }),
  containerListDir: (sessionId: string, containerId: string, path: string) =>
    call("docker_container_list_dir", { sessionId, containerId, path }),
  composeStacks: (sessionId: string) =>
    call("docker_compose_stacks", { sessionId }) as Promise<ComposeOverview>,
  composeAction: (
    sessionId: string,
    project: string,
    action: "up" | "down" | "restart" | "pull",
    productionConfirmation?: DockerProductionConfirmation,
  ) =>
    call("docker_compose_action", {
      sessionId,
      project,
      action,
      ...(productionConfirmation ? { productionConfirmation } : {}),
    }),
  composeLogsAttach: (sessionId: string, project: string, tail: number, channel: unknown) =>
    call("docker_compose_logs_attach", { sessionId, project, tail, channel, clientId: clientId() }),
  composeRedeployPrepare: (request: ComposeRedeployPrepareRequest) =>
    call("docker_compose_redeploy_prepare", request) as Promise<{ ticket: string; contentHash: string }>,
  composeRedeployAttach: (request: ComposeRedeployAttachRequest) => {
    const { channel, ...body } = request;
    return call("docker_compose_redeploy_attach", { ...body, channel, clientId: clientId() });
  },
  diskUsage: (sessionId: string) =>
    call("docker_disk_usage", { sessionId }) as Promise<DockerDiskUsage>,
  prunePreview: (sessionId: string, kinds: DockerPruneKind[]) =>
    call("docker_prune_preview", { sessionId, kinds }) as Promise<DockerPrunePreview>,
  prune: (
    sessionId: string,
    token: string,
    digest: string,
    productionConfirmation?: DockerProductionConfirmation,
  ) =>
    call("docker_prune", {
      sessionId,
      token,
      digest,
      ...(productionConfirmation ? { productionConfirmation } : {}),
    }) as Promise<DockerPruneResult>,
  reclaimable: (sessionId: string) =>
    call("docker_reclaimable", { sessionId }) as Promise<{
      totalBytes: number;
      categories: DiskCategory[];
    }>,
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
  connect: (assetId: string) => call("db_connect", { args: { assetId } }),
  disconnect: (connId: string) => call("db_disconnect", { connId }) as Promise<void>,
  schemas: (connId: string) => call("db_schemas", { connId }),
  tables: (connId: string, schema?: string) => call("db_tables", { connId, schema }),
  columns: (connId: string, table: string, schema?: string) =>
    call("db_columns", { connId, table, schema }) as Promise<TableDescribe>,
  query: (connId: string, sql: string, timeoutMs?: number) =>
    call("db_query", { connId, sql, timeoutMs }) as Promise<QueryResult>,
  redisScan: (connId: string, cursor?: number, pattern?: string, count?: number) =>
    call("redis_scan", { connId, cursor, pattern, count }),
  redisInspect: (connId: string, key: string) =>
    call("redis_inspect", { connId, key }) as Promise<import("./types").RedisKeyViewDto>,
  redisCommand: (connId: string, args: string[]) => call("redis_command", { connId, args }),
  redisSetTtl: (connId: string, key: string, seconds: number) =>
    call("redis_set_ttl", { connId, key, seconds }) as Promise<void>,
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
  // EnvironmentNames are the actual environment names the user typed back
  // for a production-classified target; they are validated against the
  // challenge's required names at resume and are never auto-echoed.
  environmentNames?: string[];
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
  // compactConversation folds the conversation's history into one saved summary.
  compactConversation: (conversationId: string) =>
    call<{ messages: number }>("ai_conversation_compact", { conversationId }),
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
  status: () => call("vault_status") as Promise<VaultStatus>,
  initMaster: (password: string) => call("vault_init_master", { password }) as Promise<void>,
  initDpapi: (password = "") => call("vault_init_dpapi", { password }) as Promise<void>,
  unlock: (password: string) => call("vault_unlock", { password }) as Promise<void>,
  lock: () => call("vault_lock") as Promise<void>,
  setAutoLock: (minutes: number) => call("vault_set_autolock", { minutes }) as Promise<void>,
  changePassword: (oldPassword: string, newPassword: string) =>
    call("vault_change_password", { oldPassword, newPassword }) as Promise<void>,
  setCredential: (
    name: string,
    kind: string,
    secret: string,
    extra: { id?: string; source?: CredentialSource; passphrase?: string } = {},
  ) =>
    call("vault_set_credential", {
      args: {
        id: extra.id,
        name,
        kind,
        secret,
        source: extra.source,
        passphrase: extra.passphrase,
      },
    }) as Promise<{ id: string }>,
  listCredentials: () => call("vault_list_credentials") as Promise<Credential[]>,
  deleteCredential: (id: string) => call("vault_delete_credential", { id }) as Promise<void>,
  revealCredential: (id: string) => call("vault_reveal_credential", { id }) as Promise<RevealedCredential>,
  updateCredential: (
    id: string,
    patch: {
      name?: string;
      secret?: string;
      source?: CredentialSource;
      passphrase?: string;
    },
  ) => call("credential_update", { args: { id, ...patch } }) as Promise<void>,
  generateKey: (name: string, algorithm?: "ed25519" | "rsa", passphrase?: string) =>
    call("vault_generate_key", {
      args: { name, algorithm, passphrase },
    }) as Promise<import("./types").GeneratedKeyDto>,
};

export type SshImportSource = "ssh-config" | "termius" | "ssh-home";
export type SshImportItemAction = "import" | "overwrite" | "skip";

export const sshImportApi = {
  preview: (args: { source: SshImportSource; path?: string; confirmed?: boolean }) =>
    call("ssh_import_preview", { args }) as Promise<import("./types").SshImportPreviewDto>,
  apply: (args: {
    source: SshImportSource;
    path?: string;
    confirmed?: boolean;
    groupId?: string;
    tags?: string;
    hosts: { id: string; action: SshImportItemAction }[];
    keys: { id: string; action: SshImportItemAction }[];
  }) => call("ssh_import_apply", { args }) as Promise<import("./types").SshImportApplyDto>,
};

export type ForwardSpec = import("./types").ForwardSpecDto & { listenHost?: string };

export const forwardApi = {
  env: (): Promise<import("./types").ForwardEnvDto> => call("forward_env"),
  create: (sessionId: string, listenPort: number, targetHost: string, targetPort: number) =>
    call("forward_create", {
      sessionId,
      listenPort,
      targetHost,
      targetPort,
    }) as Promise<ForwardSpec>,
  createSocks: (sessionId: string, listenPort: number, acknowledgeRisk = false) =>
    call("forward_create_socks", {
      sessionId,
      listenPort,
      ...(acknowledgeRisk ? { acknowledgeRisk: true } : {}),
    }) as Promise<ForwardSpec>,
  createRemote: (
    sessionId: string,
    bindHost: string,
    bindPort: number,
    targetHost: string,
    targetPort: number,
    acknowledgeRisk = false,
  ) =>
    call("forward_create_remote", {
      sessionId,
      bindHost,
      bindPort,
      targetHost,
      targetPort,
      ...(acknowledgeRisk ? { acknowledgeRisk: true } : {}),
    }) as Promise<ForwardSpec>,
  list: () => call("forward_list") as Promise<ForwardSpec[]>,
  retry: (id: string, sessionId?: string) => call("forward_retry", {
    id,
    ...(sessionId ? { sessionId } : {}),
  }) as Promise<ForwardSpec>,
  remove: (id: string) => call("forward_remove", { id }) as Promise<void>,
};

export const syncApi = {
  digest: () => call("sync_digest") as Promise<import("./types").SyncDigest>,

  exportAssets: (assetIds: string[], withCreds: boolean) =>
    call("sync_export", { args: { assetIds, withCreds } }) as Promise<import("./types").SyncBundle>,

  importBundle: (bundle: import("./types").SyncBundle, force: boolean) =>
    call("sync_import", { args: { bundle, force } }) as Promise<import("./types").ImportReport>,

  readBundleFile: (path: string, password?: string) =>
    call("sync_bundle_read", {
      args: { path, password } as import("./generated/types").BundleFileRequest,
    }),

  writeBundleFile: (path: string, content: string, password?: string) =>
    call("sync_bundle_write", {
      args: { path, content, password },
    }) as Promise<import("./types").SyncBundleWriteResult>,

  linkGet: () => call("sync_link_get") as Promise<import("./types").AccountLink>,
  linkSet: (patch: { url?: string; username?: string; password?: string; insecure?: boolean }) =>
    call("sync_link_set", {
      args: patch as unknown as import("./generated/types").LinkPatch,
    }) as Promise<import("./types").AccountLink>,

  status: () => call("sync_status") as Promise<import("./types").SyncStatus>,
  wake: () => ignoreResult(call("sync_wake")),

  applyObjects: (objects: import("./types").SyncApplyObject[]) =>
    call("sync_apply_objects", { args: { objects } }) as Promise<import("./types").SyncApplyResult>,

  collectAssets: (includeDeleted: boolean, afterId?: string, limit?: number) =>
    call("sync_collect_assets", {
      args: { includeDeleted, afterId, limit },
    }) as Promise<import("./types").SyncCollectAssetsResult>,

  collectTombstones: (afterId?: string, limit?: number) =>
    call("sync_collect_tombstones", {
      args: { afterId, limit },
    }) as Promise<import("./types").SyncCollectTombstonesResult>,

  collectCredentials: (revealSecrets: boolean, afterId?: string, limit?: number) =>
    call("sync_collect_credentials", {
      args: { revealSecrets, afterId, limit },
    }) as Promise<import("./types").SyncCollectCredentialsResult>,

  collectKnownHosts: (afterId?: string, limit?: number) =>
    call("sync_collect_known_hosts", {
      args: { afterId, limit },
    }) as Promise<import("./types").SyncCollectKnownHostsResult>,

  collectAIProfiles: (revealSecrets: boolean, afterId?: string, limit?: number) =>
    call("sync_collect_ai_profiles", {
      args: { revealSecrets, afterId, limit },
    }) as Promise<import("./types").SyncCollectAIProfilesResult>,

  collectAIPermission: () =>
    call("sync_collect_ai_permission") as Promise<import("./types").SyncCollectAIPermission>,

  collectAIGrants: () => call("sync_collect_ai_grants") as Promise<import("./types").SyncCollectAIGrants>,

  collectPreferences: () => call("sync_collect_preferences") as Promise<import("./types").SyncCollectPreference[]>,

  preferencesGet: () => call("sync_preferences_get") as Promise<import("./types").PreferenceScopeView>,

  preferencesUpdate: (update: { set?: Record<string, unknown>; clear?: string[] }) =>
    call("sync_preferences_set", {
      args: update as import("./generated/types").PreferenceUpdate,
    }) as Promise<import("./types").PreferenceScopeView>,

  kindOptInGet: () => call("sync_kind_opt_in_get") as Promise<import("./types").SyncKindOptIn>,

  kindOptInSet: (patch: Partial<import("./types").SyncKindOptIn>) =>
    call("sync_kind_opt_in_set", { args: patch }) as Promise<import("./types").SyncKindOptIn>,
};
