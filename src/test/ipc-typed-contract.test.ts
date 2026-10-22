import * as ts from "typescript";
import { describe, expect, it } from "vitest";

const root = ts.sys.getCurrentDirectory();
const resolve = ts.sys.resolvePath;
const configPath = resolve("tsconfig.json");
const config = ts.readConfigFile(configPath, ts.sys.readFile);
if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, "\n"));
const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root);

// The contract probes only exercise the IPC type surface; dropping the
// vite/client ambient types trims roughly a quarter of the in-memory compile
// cost without weakening the checks (any missing global would surface as a
// compile error here).
const options: ts.CompilerOptions = { ...parsed.options, types: [] };

function compile(rootNames: string[], overlays: Record<string, string> = {}): readonly ts.Diagnostic[] {
  const normalizedOverlays = new Map(
    Object.entries(overlays).map(([fileName, source]) => [resolve(fileName), source]),
  );
  const host = ts.createCompilerHost(options);
  const readFile = host.readFile.bind(host);
  const fileExists = host.fileExists.bind(host);
  host.readFile = (fileName) => normalizedOverlays.get(resolve(fileName)) ?? readFile(fileName);
  host.fileExists = (fileName) => normalizedOverlays.has(resolve(fileName)) || fileExists(fileName);
  host.getSourceFile = (fileName, languageVersion) => {
    const source = host.readFile(fileName);
    return source === undefined ? undefined : ts.createSourceFile(fileName, source, languageVersion, true);
  };
  const program = ts.createProgram({
    rootNames,
    options,
    host,
  });
  return ts.getPreEmitDiagnostics(program);
}

function formatted(diagnostics: readonly ts.Diagnostic[]): string {
  return ts.formatDiagnostics(diagnostics, {
    getCanonicalFileName: (fileName) => fileName,
    getCurrentDirectory: () => root,
    getNewLine: () => "\n",
  });
}

// ts.sys.resolvePath keeps the platform separator while the compiler reports
// file names with normalized forward slashes; compare on separator form so the
// assertion holds on Windows without weakening the check.
function sameFileName(a: string, b: string): boolean {
  return a.replaceAll("\\", "/") === b.replaceAll("\\", "/");
}

describe("typed IPC call contract", () => {
  // These tests run a real in-memory TypeScript compile of the generated IPC
  // graph — deterministic CPU work, not an async wait that could be awaited
  // away. Measured cost with the types:[] trim above: ~2.5s standalone,
  // 7-13s under heavy host contention (load average 45-60 from sibling test
  // runs). The explicit 30s budget is sized to that measured cost (roughly
  // 12x headroom over standalone, 2.3x over the worst observed load); it is a
  // compute budget for each compile itself, authorized for these
  // compile-type tests only, not a behavioral threshold and not a global
  // timeout loosening.
  const COMPILE_BUDGET_MS = 30_000;

  it("rejects unknown, dynamic and incorrectly shaped calls while preserving current legacy-command overloads", () => {
    const probe = resolve("src/test/__ipc_typed_contract_probe.ts");
    const source = `
import { call } from "../ipc/commands";
import * as commands from "../ipc/commands";
const dynamic: string = "exec_batch_execute";
// @ts-expect-error unknown commands are rejected
void call("definitely_unknown");
// @ts-expect-error dynamic strings are rejected
void call(dynamic);
// @ts-expect-error generated input fields are checked
void call("exec_batch_execute", { batchID: "b", sessionIds: ["s"], command: "pwd" });
// @ts-expect-error unrelated fsRequest fields are not part of fs_cancel
void call("fs_cancel", { taskId: "t", path: "/" });
// @ts-expect-error nested commands retain their envelope
void call("host_share_terminal_open", { deviceId: "d", streamId: "s" });
async function outputFields() {
  const batch = await call("exec_batch_execute", { batchId: "b", sessionIds: ["s"], command: "pwd" });
  // @ts-expect-error generated output fields are checked
  return batch.batchID;
}
void outputFields;
void call("exec_batch_execute", { batchId: "b", sessionIds: ["s"], command: "pwd" });
void call("fs_cancel", { taskId: "t" });
void call("host_share_terminal_open", {
  args: { deviceId: "d", streamId: "s" },
  channel: { toJSON: () => null },
});
void call("app_platform");
// @ts-expect-error the current facade has no dynamic-command escape hatch
void commands.unsafeCall(dynamic);
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("surfaces an in-memory generated DTO rename in the real exec batch caller", () => {
    const consumer = resolve("src/ipc/execBatchApi.ts");
    const generated = resolve("src/ipc/generated/types.ts");
    const baseline = compile([consumer]);
    expect(baseline, formatted(baseline)).toEqual([]);

    const source = ts.sys.readFile(generated);
    if (source === undefined) throw new Error("generated IPC types are missing");
    const diagnostics = compile([consumer], { [generated]: source.replaceAll("batchId", "batchID") });
    expect(diagnostics.length).toBeGreaterThan(0);
    expect(diagnostics.some((diagnostic) => diagnostic.file !== undefined && sameFileName(diagnostic.file.fileName, consumer))).toBe(true);
  }, COMPILE_BUDGET_MS);

  it("types the remote desktop vertical while preserving its public facade contracts", () => {
    const probe = resolve("src/test/__ipc_remote_desktop_contract_probe.ts");
    const source = `
import { call } from "../ipc/commands";
import {
  remoteAccountApi,
  remoteFleetApi,
  remoteLinkApi,
  remotePreviewApi,
  syncCompareApi,
  type RemoteLinkResult,
  type RemotePreviewCreateResult,
  type SyncCompareResult,
} from "../ipc/remoteApi";
import type { AccountUser } from "../ipc/authApi";
import type { FleetDevice, FleetMetricsResponse } from "../ipc/fleetApi";

const devices: Promise<{ devices: FleetDevice[] }> = remoteFleetApi.devices();
const metrics: Promise<FleetMetricsResponse> = remoteFleetApi.metrics("device-1");
const me: Promise<{ user: AccountUser; mfa_required: boolean }> = remoteAccountApi.me();
const linked: Promise<RemoteLinkResult> = remoteLinkApi.mfa("123456");
const compared: Promise<SyncCompareResult> = syncCompareApi.compare();
const preview: Promise<RemotePreviewCreateResult> = remotePreviewApi.create("tab-1", 60_000);
void [devices, metrics, me, linked, compared, preview];

// @ts-expect-error the facade keeps only the two supported admin state actions
void remoteAccountApi.adminSetUserState("u-1", "active");
// @ts-expect-error the facade keeps repair actions restricted
void syncCompareApi.repairUndecryptable({ id: "a1", action: "delete", userId: "u-1", compareToken: "token", remoteSeq: 1, remoteBlobHash: "hash" });

// @ts-expect-error nested commands reject flat input
void call("preview_share_active", { tabId: "tab-1" });
// @ts-expect-error no-argument commands reject unexpected input
void call("remote_healthz", { unexpected: true });
// @ts-expect-error generated optional fields retain their types
void call("remote_fleet_enroll_code", { args: { ttlMs: 60_000, userId: 42 } });
// @ts-expect-error generated numeric fields retain their types
void call("remote_fleet_device_metrics", { args: { deviceId: "device-1", sinceMs: "30_000" } });
// @ts-expect-error generated MFA input is checked
void call("sync_link_mfa", { args: { code: 123456 } });
// @ts-expect-error generated preview input is checked
void call("preview_share_create", { args: { tabId: "tab-1", ttlMs: "one-hour" } });
// @ts-expect-error generated compare evidence is checked
void call("sync_repair_undecryptable", { args: { id: "a1", action: "clear-cloud", userId: "u-1", compareToken: "token", remoteSeq: "1", remoteBlobHash: "hash" } });

void call("remote_healthz");
void call("sync_compare");
void call("remote_fleet_enroll_code", { args: { ttlMs: 60_000 } });
void call("remote_fleet_device_metrics", { args: { deviceId: "device-1", sinceMs: 30_000 } });
void call("sync_link_mfa", { args: { code: "123456" } });
void call("sync_link_reset_password", { args: { newPassword: "new-password" } });
void call("preview_share_create", { args: { tabId: "tab-1", ttlMs: 60_000 } });
void call("sync_repair_undecryptable", { args: { id: "a1", action: "clear-cloud", userId: "u-1", compareToken: "token", remoteSeq: 1, remoteBlobHash: "hash" } });
void call<unknown>("app_info");

async function generatedOutputs() {
  const link = await call("sync_link_mfa", { args: { code: "123456" } });
  const url: string = link.url;
  const recoveryKey: string | undefined = link.recoveryKey;
  const compare = await call("sync_compare");
  const compareToken: string = compare.compareToken;
  const remoteBlobHash: string | undefined = compare.rows[0].remoteBlobHash;
  // @ts-expect-error generated output fields are checked
  const invalidToken: number = compare.compareToken;
  void [url, recoveryKey, compareToken, remoteBlobHash, invalidToken];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types the complete sync vertical while preserving facade compatibility and dynamic unknowns", () => {
    const probe = resolve("src/test/__ipc_sync_vertical_contract_probe.ts");
    const source = `
import { call, syncApi } from "../ipc/commands";
import { remoteLinkApi, type RemoteLinkResult } from "../ipc/remoteApi";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type {
  AccountLink,
  SyncApplyResult,
  SyncBundleWriteResult,
  SyncCollectCredentialsResult,
  SyncDigest,
  SyncStatus,
} from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type DynamicChecks = [
  Assert<Equal<IPCInput<"sync_apply_objects">["objects"][number]["payload"], unknown>>,
  Assert<Equal<IPCOutput<"sync_collect_preferences">[number]["value"], unknown>>,
  Assert<Equal<IPCOutput<"sync_preferences_get">["effective"][string], unknown>>,
];
const dynamicChecks: DynamicChecks = [true, true, true];
void dynamicChecks;

const digest: Promise<SyncDigest> = syncApi.digest();
const written: Promise<SyncBundleWriteResult> = syncApi.writeBundleFile("/tmp/sync-bundle.nex", "bundle-content");
const linked: Promise<AccountLink> = syncApi.linkSet({ username: "alice" });
const remoteLinked: Promise<RemoteLinkResult> = remoteLinkApi.linkSet({ password: "updated-password" });
const status: Promise<SyncStatus> = syncApi.status();
const applied: Promise<SyncApplyResult> = syncApi.applyObjects([{ id: "object-1", kind: "preference", payload: { free: ["form", null] } }]);
const credentials: Promise<SyncCollectCredentialsResult> = syncApi.collectCredentials(false);
void [digest, written, linked, remoteLinked, status, applied, credentials];
void syncApi.readBundleFile("/tmp/sync-bundle.nex");
void syncApi.preferencesUpdate({ clear: ["theme"] });
void syncApi.kindOptInSet({ knownHost: false });

declare const linkPatch: IPCInput<"sync_link_set">;
declare const bundleRead: IPCInput<"sync_bundle_read">;
declare const preferenceUpdate: IPCInput<"sync_preferences_set">;
void call("sync_digest");
void call("sync_link_get");
void call("sync_link_set", { args: linkPatch });
void call("sync_bundle_read", { args: bundleRead });
void call("sync_preferences_set", { args: preferenceUpdate });
void call("sync_wake");
void call("sync_apply_objects", { args: { objects: [{ id: "object-1", kind: "preference", payload: { free: ["form", null] } }] } });
void call("sync_kind_opt_in_set", { args: { knownHost: false } });
void call("sync_collect_credentials", { args: { revealSecrets: false } });
void call("sync_export", { args: { assetIds: ["asset-1"], withCreds: false } });

// @ts-expect-error typed sync commands reject the legacy generic output escape
void call<{ connect: string }>("sync_link_set", { args: linkPatch });
// @ts-expect-error generated link patch fields are checked
void call("sync_link_set", { args: { url: 42 } });
// @ts-expect-error nested link patches reject flat input
void call("sync_link_set", { url: "https://sync.example.com" });
// @ts-expect-error owner identity is injected by the authenticated session
void call("sync_kind_opt_in_set", { args: { knownHost: true, userId: "other-user" } });
// @ts-expect-error secret reveal flags remain boolean
void call("sync_collect_credentials", { args: { revealSecrets: "yes" } });
// @ts-expect-error credential export flags remain boolean
void call("sync_export", { args: { assetIds: ["asset-1"], withCreds: "yes" } });

async function generatedOutputs() {
  const link = await call("sync_link_get");
  const hasPassword: boolean = link.hasPassword;
  // @ts-expect-error the encrypted link view never exposes its stored password
  const plaintextPassword = link.password;
  const linked = await call("sync_link_set", { args: linkPatch });
  const mfaTicket: string | undefined = linked.mfaTicket;
  const recoveryKey: string | undefined = linked.recoveryKey;
  const bundle = await call("sync_export", { args: { assetIds: ["asset-1"], withCreds: false } });
  const legacyTombstones: { id: string; deletedAt: number }[] | undefined = bundle.credTombstones;
  const imported = await call("sync_import", { args: { bundle, force: false } });
  const orphaned: number = imported.credsOrphaned;
  const collected = await call("sync_collect_credentials", { args: { revealSecrets: false } });
  const secret: string | null | undefined = collected.credentials[0].secret;
  void [hasPassword, plaintextPassword, mfaTicket, recoveryKey, legacyTombstones, orphaned, secret];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types cron and logTail while preserving facade shapes and unresolved stream payloads", () => {
    const probe = resolve("src/test/__ipc_cron_logtail_contract_probe.ts");
    const source = `
import { call } from "../ipc/commands";
import { cronApi, type CronJob, type CronRegistration } from "../ipc/cron";
import {
  logTailApi,
  type LogTailCancelResult,
  type LogTailSaveResult,
  type LogTailSource,
  type LogTailStartResult,
} from "../ipc/logTailApi";
import { ipcContractManifest, type IPCInput, type IPCOutput } from "../ipc/generated/manifest";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type ContractChecks = [
  Assert<Equal<IPCOutput<"cron_unregister">, unknown>>,
  Assert<Equal<(typeof ipcContractManifest.commands)["log_tail_start"]["streamPayload"], "unknown">>,
  Assert<Equal<IPCOutput<"cron_get">["ownerId"], string>>,
  Assert<Equal<IPCOutput<"cron_get">["ownerKind"], string>>,
  Assert<Equal<IPCOutput<"cron_get">["run"]["scheduledFor"], string>>,
  Assert<Equal<IPCInput<"log_tail_start">["maxConcurrent"], number | undefined>>,
];
const contractChecks: ContractChecks = [true, true, true, true, true, true];
void contractChecks;

const registration: CronRegistration = { sessionId: "conversation-1", prompt: "check disk", schedule: "0 2 * * *" };
const created: Promise<CronJob> = cronApi.register(registration);
const listed: Promise<CronJob[]> = cronApi.list("conversation-1");
const fetched: Promise<CronJob> = cronApi.get("conversation-1", "job-1");
const toggled: Promise<CronJob> = cronApi.setEnabled("conversation-1", "job-1", false);
const unregistered: Promise<void> = cronApi.unregister("conversation-1", "job-1");
const source: LogTailSource = { sourceId: "source-1", sessionId: "session-1", kind: "file", value: "/var/log/app.log" };
const started: Promise<LogTailStartResult> = logTailApi.start({ runId: "run-1", sources: [source] }, {});
const canceled: Promise<LogTailCancelResult> = logTailApi.cancel("run-1");
const saved: Promise<LogTailSaveResult> = logTailApi.save("/tmp/merged.log", "first\\n");
void [created, listed, fetched, toggled, unregistered, started, canceled, saved];

void call("cron_register", { sessionId: "conversation-1", prompt: "check disk", schedule: "0 2 * * *", disabled: false });
void call("cron_get", { sessionId: "conversation-1", jobId: "job-1" });
void call("cron_set_enabled", { sessionId: "conversation-1", jobId: "job-1", enabled: true });
void call("cron_unregister", { sessionId: "conversation-1", jobId: "job-1" });
void call("log_tail_start", { runId: "run-1", sources: [source], channel: {} });
void call("log_tail_cancel", { runId: "run-1" });
void call("log_tail_save", { path: "/tmp/merged.log", contentBase64: "Zmlyc3QK" });

// @ts-expect-error typed cron commands reject the legacy generic output escape
void call<CronJob>("cron_get", { sessionId: "conversation-1", jobId: "job-1" });
// @ts-expect-error typed logTail commands reject the legacy generic output escape
void call<LogTailCancelResult>("log_tail_cancel", { runId: "run-1" });
// @ts-expect-error owner identity is not accepted from the registration facade input
void call("cron_register", { sessionId: "conversation-1", prompt: "check disk", schedule: "0 2 * * *", ownerId: "other-user" });
// @ts-expect-error cron enabled state remains boolean
void call("cron_set_enabled", { sessionId: "conversation-1", jobId: "job-1", enabled: "yes" });
// @ts-expect-error logTail concurrency remains numeric
void call("log_tail_start", { runId: "run-1", sources: [source], maxConcurrent: "four", channel: {} });
// @ts-expect-error logTail start requires its existing JSON stream channel
void call("log_tail_start", { runId: "run-1", sources: [source] });
// @ts-expect-error logTail save content remains base64 text
void call("log_tail_save", { path: "/tmp/merged.log", contentBase64: 42 });
// @ts-expect-error the public source facade keeps its supported source-kind union
const invalidSource: LogTailSource = { sourceId: "source-2", sessionId: "session-1", kind: "process", value: "worker" };
void invalidSource;

async function generatedOutputs() {
  const job = await call("cron_get", { sessionId: "conversation-1", jobId: "job-1" });
  const ownerId: string = job.ownerId;
  const ownerKind: string = job.ownerKind;
  const schedule: string = job.schedule;
  const leaseOwner: string | undefined = job.lease.owner;
  const retryAt: string | undefined = job.retryAt;
  const coalesced: boolean | undefined = job.run.coalesced;
  const lastCoalesced: boolean | undefined = job.lastCoalesced;
  // @ts-expect-error generated cron output fields are checked
  const invalidRevision: string = job.revision;
  void [ownerId, ownerKind, schedule, leaseOwner, retryAt, coalesced, lastCoalesced, invalidRevision];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types asset, group and known-host commands without changing facade or trust semantics", () => {
    const probe = resolve("src/test/__ipc_asset_known_host_contract_probe.ts");
    const source = `
import { assetApi, call, type Asset, type AssetGroup, type AssetSearchResponse } from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type { AssetProbeBatchDto, KnownHostDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type ContractChecks = [
  Assert<Equal<IPCOutput<"asset_delete">, unknown>>,
  Assert<Equal<IPCOutput<"group_delete">, unknown>>,
  Assert<Equal<IPCOutput<"known_host_list">, unknown>>,
  Assert<Equal<IPCOutput<"known_host_accept">, unknown>>,
  Assert<Equal<IPCOutput<"known_host_remove">, unknown>>,
  Assert<Equal<IPCOutput<"asset_get">["options"], unknown>>,
  Assert<Equal<IPCInput<"asset_create">["options"], unknown>>,
  Assert<Equal<IPCInput<"asset_update">["options"], unknown>>,
  Assert<Equal<IPCInput<"asset_update">["groupId"], string | null | undefined>>,
  Assert<Equal<IPCInput<"asset_probe_batch">["timeoutMs"], number | undefined>>,
  Assert<Equal<IPCInput<"known_host_accept">["key"], string | undefined>>,
  Assert<Equal<IPCOutput<"asset_search_meta">["truncated"], boolean>>,
];
const contractChecks: ContractChecks = [true, true, true, true, true, true, true, true, true, true, true, true];
void contractChecks;

const listed: Promise<Asset[]> = assetApi.list();
const fetched: Promise<Asset> = assetApi.get("asset-1");
const created: Promise<Asset> = assetApi.create({ kind: "ssh", name: "Web" });
const updated: Promise<Asset> = assetApi.update({ id: "asset-1", groupId: null });
const futureUpdated: Promise<Asset> = assetApi.update({ id: "asset-1", futureField: { free: true } });
const searched: Promise<Asset[]> = assetApi.search("web");
const searchMeta: Promise<AssetSearchResponse> = assetApi.searchMeta("web");
const probed: Promise<AssetProbeBatchDto> = assetApi.probeBatch(["asset-1"]);
const groups: Promise<AssetGroup[]> = assetApi.groupList();
const groupCreated: Promise<AssetGroup> = assetApi.groupCreate("Production");
const groupUpdated: Promise<AssetGroup> = assetApi.groupUpdate("group-1", undefined, null);
const groupDeleted: Promise<void> = assetApi.groupDelete("group-1");
const knownHosts: Promise<KnownHostDto[]> = assetApi.knownHostList();
const accepted: Promise<void> = assetApi.knownHostAccept("10.0.0.8", 22, "ssh-ed25519", "SHA256:abc");
const acceptedWithKey: Promise<void> = assetApi.knownHostAccept("10.0.0.8", 22, "ssh-ed25519", "SHA256:abc", "AAAAC3NzaC1lZDI1NTE5AAAAIFakeDemoKey");
const removed: Promise<void> = assetApi.knownHostRemove("known-1");
void [listed, fetched, created, updated, futureUpdated, searched, searchMeta, probed, groups, groupCreated, groupUpdated, groupDeleted, knownHosts, accepted, acceptedWithKey, removed];

void call("asset_list");
void call("asset_get", { id: "asset-1" });
void call("asset_create", { args: { kind: "ssh", name: "Web", options: { free: [1, true, null] } } });
void call("asset_update", { args: { id: "asset-1", groupId: null } });
void call("asset_update", { args: { id: "asset-1" } });
void call("asset_read_key_file", { path: "/tmp/example-key" });
void call("asset_delete", { id: "asset-1" });
void call("asset_search", { q: "web" });
void call("asset_search_meta", { q: "web" });
void call("asset_probe_batch", { args: { assetIds: ["asset-1"] } });
void call("group_list");
void call("group_create", { name: "Production" });
void call("group_update", { id: "group-1", parentId: null });
void call("group_delete", { id: "group-1" });
void call("known_host_list");
void call("known_host_accept", { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc" });
void call("known_host_accept", { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc", key: "AAAAC3NzaC1lZDI1NTE5AAAAIFakeDemoKey" });
void call("known_host_remove", { id: "known-1" });

// @ts-expect-error typed asset commands reject the legacy generic output escape
void call<Asset[]>("asset_list");
// @ts-expect-error nested asset creation rejects flat input
void call("asset_create", { kind: "ssh", name: "Web" });
// @ts-expect-error asset creation still requires a name
void call("asset_create", { args: { kind: "ssh" } });
// @ts-expect-error partial update retains its required identity
void call("asset_update", { args: { groupId: null } });
// @ts-expect-error nullable numeric patches do not accept strings
void call("asset_update", { args: { id: "asset-1", port: "22" } });
// @ts-expect-error probe batches require asset ids
void call("asset_probe_batch", { args: { timeoutMs: 2500 } });
// @ts-expect-error known-host ports remain numeric
void call("known_host_accept", { host: "10.0.0.8", port: "22", keyType: "ssh-ed25519", fingerprint: "SHA256:abc" });
// @ts-expect-error complete public key material remains text
void call("known_host_accept", { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc", key: 42 });

async function generatedOutputs() {
  const search = await call("asset_search_meta", { q: "web" });
  const total: number = search.total;
  const truncated: boolean = search.truncated;
  const options: unknown = search.assets[0].options;
  void [total, truncated, options];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types vault and credential commands without widening secret visibility or facade inputs", () => {
    const probe = resolve("src/test/__ipc_vault_credential_contract_probe.ts");
    const source = `
import {
  call,
  vaultApi,
  type Credential,
  type RevealedCredential,
  type VaultStatus,
} from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type { GeneratedKeyDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
const passwordOptional: Assert<Equal<IPCInput<"vault_init_master">["password"], string | undefined>> = true;
const setPassphraseNullable: Assert<Equal<IPCInput<"vault_set_credential">["passphrase"], string | null | undefined>> = true;
const updateSourceNullable: Assert<Equal<IPCInput<"credential_update">["source"], string | null | undefined>> = true;
const generatePassphraseOptional: Assert<Equal<IPCInput<"vault_generate_key">["passphrase"], string | undefined>> = true;
const autoLockNullable: Assert<Equal<IPCInput<"vault_set_autolock">["minutes"], number | null>> = true;
const revealPassphraseNullable: Assert<Equal<IPCOutput<"vault_reveal_credential">["passphrase"], string | null>> = true;
const listSourceNullable: Assert<Equal<IPCOutput<"vault_list_credentials">[number]["source"], string | null>> = true;
const generatedPublicKey: Assert<Equal<IPCOutput<"vault_generate_key">["publicKey"], string>> = true;
const lockUnknown: Assert<Equal<IPCOutput<"vault_lock">, unknown>> = true;
const updateUnknown: Assert<Equal<IPCOutput<"credential_update">, unknown>> = true;
void [passwordOptional, setPassphraseNullable, updateSourceNullable, generatePassphraseOptional, autoLockNullable, revealPassphraseNullable, listSourceNullable, generatedPublicKey, lockUnknown, updateUnknown];

const status: Promise<VaultStatus> = vaultApi.status();
const initialized: Promise<void> = vaultApi.initMaster("");
const initializedDpapi: Promise<void> = vaultApi.initDpapi();
const unlocked: Promise<void> = vaultApi.unlock("");
const locked: Promise<void> = vaultApi.lock();
const autoLock: Promise<void> = vaultApi.setAutoLock(0);
const changed: Promise<void> = vaultApi.changePassword("", "");
const created: Promise<{ id: string }> = vaultApi.setCredential("database", "password", "");
const listedFacade: Promise<Credential[]> = vaultApi.listCredentials();
const deleted: Promise<void> = vaultApi.deleteCredential("cred-1");
const revealedFacade: Promise<RevealedCredential> = vaultApi.revealCredential("cred-1");
const updated: Promise<void> = vaultApi.updateCredential("cred-1", { passphrase: "" });
const generatedFacade: Promise<GeneratedKeyDto> = vaultApi.generateKey("default-key");
void [status, initialized, initializedDpapi, unlocked, locked, autoLock, changed, created, listedFacade, deleted, revealedFacade, updated, generatedFacade];

void call("vault_status");
void call("vault_init_master", { password: "" });
void call("vault_init_dpapi", {});
void call("vault_unlock", { password: "" });
void call("vault_lock");
void call("vault_set_autolock", { minutes: 0 });
void call("vault_change_password", { oldPassword: "", newPassword: "" });
void call("vault_set_credential", { args: { name: "database", kind: "password", secret: "" } });
void call("vault_set_credential", { args: { name: "key", kind: "private_key", secret: "fake", source: null, passphrase: null } });
void call("vault_list_credentials");
void call("vault_delete_credential", { id: "cred-1" });
void call("vault_reveal_credential", { id: "cred-1" });
void call("credential_update", { args: { id: "cred-1", passphrase: "" } });
void call("credential_update", { args: { id: "cred-1", source: "" } });
void call("credential_update", { args: { id: "cred-1", source: null } });
void call("vault_generate_key", { args: { name: "default-key" } });

// @ts-expect-error typed vault commands reject the legacy generic output escape
void call<VaultStatus>("vault_status");
// @ts-expect-error typed credential updates reject the legacy generic output escape
void call<void>("credential_update", { args: { id: "cred-1" } });
// @ts-expect-error nested credential creation rejects flat input
void call("vault_set_credential", { name: "database", kind: "password", secret: "" });
// @ts-expect-error credential creation still requires secret content, including an explicit empty string
void call("vault_set_credential", { args: { name: "database", kind: "password" } });
// @ts-expect-error auto-lock minutes remain required by the facade wire call
void call("vault_set_autolock", {});
// @ts-expect-error password fields remain strings
void call("vault_unlock", { password: 42 });
// @ts-expect-error generated-key names remain strings
void call("vault_generate_key", { args: { name: 42 } });
// @ts-expect-error the existing update facade does not widen patches to nullable wire values
void vaultApi.updateCredential("cred-1", { passphrase: null });

async function generatedOutputs() {
  const status = await call("vault_status");
  const mode: string = status.mode;
  // @ts-expect-error vault status never exposes password material
  const password = status.password;
  const listed = await call("vault_list_credentials");
  const usedBy: { id: string; name: string; kind: string }[] = listed[0].usedBy;
  const hasPassphrase: boolean = listed[0].hasPassphrase;
  // @ts-expect-error credential list metadata never exposes secret values
  const secret = listed[0].secret;
  const revealed = await call("vault_reveal_credential", { id: "cred-1" });
  const value: string = revealed.value;
  const passphrase: string | null = revealed.passphrase;
  const generated = await call("vault_generate_key", { args: { name: "default-key" } });
  const publicKey: string = generated.publicKey;
  // @ts-expect-error generated-key results contain public material only
  const privateKey = generated.privateKey;
  const created = await call("vault_set_credential", { args: { name: "database", kind: "password", secret: "" } });
  const id: string = created.id;
  void [mode, password, usedBy, hasPassphrase, secret, value, passphrase, publicKey, privateKey, id];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types fs, desktop local-file and mount commands without widening paths, bytes or secrets", () => {
    const probe = resolve("src/test/__ipc_fs_local_mount_contract_probe.ts");
    const source = `
import {
  call,
  fsApi,
  localFileApi,
  mountApi,
  type FileRootsDto,
  type LocalFolderListDto,
} from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type { FileEntryDto, MountEntryDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
const readLimit: Assert<Equal<IPCInput<"fs_read">["maxBytes"], number | undefined>> = true;
const rangeLimit: Assert<Equal<IPCInput<"local_file_read_range">["maxBytes"], number | undefined>> = true;
const resume: Assert<Equal<IPCInput<"fs_upload">["resume"], boolean | undefined>> = true;
const attempt: Assert<Equal<IPCInput<"fs_upload">["attemptId"], string | undefined>> = true;
const chunkAttempt: Assert<Equal<IPCInput<"fs_upload_chunk">["attemptId"], string>> = true;
const chunkBytes: Assert<Equal<IPCInput<"fs_upload_chunk">["contentBase64"], string>> = true;
const mountPassword: Assert<Equal<IPCInput<"mount_create">["password"], string | undefined>> = true;
const readBytes: Assert<Equal<IPCOutput<"fs_read">["contentBase64"], string>> = true;
const truncated: Assert<Equal<IPCOutput<"fs_read_range">["truncated"], boolean>> = true;
const writeUnknown: Assert<Equal<IPCOutput<"fs_write">, unknown>> = true;
const removeUnknown: Assert<Equal<IPCOutput<"mount_remove">, unknown>> = true;
const capability: Assert<Equal<IPCOutput<"mount_capability">, string | null>> = true;
void [readLimit, rangeLimit, resume, attempt, chunkAttempt, chunkBytes, mountPassword, readBytes, truncated, writeUnknown, removeUnknown, capability];

const roots: Promise<FileRootsDto> = fsApi.roots("session-1");
const entries: Promise<FileEntryDto[]> = fsApi.list("session-1", "/remote");
const written: Promise<void> = fsApi.write("session-1", "/remote/a.bin", "AAEC/w==");
const uploaded: Promise<number> = fsApi.upload("session-1", "C:\\local\\a.bin", "/remote/a.bin");
const canceled: Promise<boolean> = fsApi.cancel("task-1");
const folder: Promise<LocalFolderListDto> = localFileApi.listFolder("C:\\local\\pics");
const mounts: Promise<MountEntryDto[]> = mountApi.list({ sessionId: "session-1" });
const mounted: Promise<MountEntryDto> = mountApi.create({ sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:" });
const removed: Promise<void> = mountApi.remove("Z:");
void [roots, entries, written, uploaded, canceled, folder, mounts, mounted, removed];

void call("fs_read", { sessionId: "session-1", path: "/remote/a.bin" });
void call("fs_read", { sessionId: "session-1", path: "/remote/a.bin", maxBytes: 0 });
void call("fs_read_range", { sessionId: "session-1", path: "/remote/a.bin", offset: 0 });
void call("fs_write", { args: { sessionId: "session-1", path: "/remote/a.bin", contentBase64: "AAEC/w==", backup: true } });
void call("fs_upload", { sessionId: "session-1", localPath: "C:\\local\\a.bin", remotePath: "/remote/a.bin" });
void call("fs_upload", { sessionId: "session-1", localPath: "C:\\local\\a.bin", remotePath: "/remote/a.bin", resume: false, taskId: "task-1", attemptId: "attempt-1" });
void call("fs_upload_begin", { sessionId: "session-1", remotePath: "/remote/a.bin", totalSize: 4, taskId: "task-1", attemptId: "attempt-1" });
void call("fs_upload_chunk", { sessionId: "session-1", uploadId: "upload-1", taskId: "task-1", attemptId: "attempt-1", index: 0, size: 4, sha256: "3d1f57c984978ef98a18378c8166c1cb8ede02c03eeb6aee7e2f121dfeee3e56", contentBase64: "AAEC/w==" });
void call("fs_upload_commit", { sessionId: "session-1", uploadId: "upload-1", taskId: "task-1", attemptId: "attempt-1" });
void call("fs_upload_abort", { sessionId: "session-1", uploadId: "upload-1", taskId: "task-1", attemptId: "attempt-1" });
void call("fs_pack_download", { sessionId: "session-1", remotePath: "/remote/dir", localPath: "C:\\local\\dir.tar.gz" });
void call("local_file_read", { path: "C:\\local\\a.bin", maxBytes: 0 });
void call("local_file_read_range", { path: "C:\\local\\a.bin", offset: 0 });
void call("mount_capability");
void call("mount_list", { sessionId: "session-1", forceRefresh: false });
void call("mount_create", { args: { sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:", password: "" } });
void call("mount_remove", { localPoint: "Z:" });

// @ts-expect-error typed fs commands reject the legacy generic output escape
void call<{ path: string }>("fs_read", { sessionId: "session-1", path: "/remote/a.bin" });
// @ts-expect-error reads do not acquire upload-only local paths
void call("fs_read", { sessionId: "session-1", path: "/remote/a.bin", localPath: "C:\\local\\a.bin" });
// @ts-expect-error remote read paths remain required
void call("fs_read", { sessionId: "session-1" });
// @ts-expect-error optional read limits remain numeric
void call("fs_read", { sessionId: "session-1", path: "/remote/a.bin", maxBytes: "8MiB" });
// @ts-expect-error nested writes reject flat input
void call("fs_write", { sessionId: "session-1", path: "/remote/a.bin", contentBase64: "AAEC/w==", backup: true });
// @ts-expect-error the facade's backup=true default does not make the wire field optional
void call("fs_write", { args: { sessionId: "session-1", path: "/remote/a.bin", contentBase64: "AAEC/w==" } });
// @ts-expect-error native and staged local paths remain strings
void call("fs_upload", { sessionId: "session-1", localPath: 42, remotePath: "/remote/a.bin" });
// @ts-expect-error upload begin does not acquire chunk content identity
void call("fs_upload_begin", { sessionId: "session-1", remotePath: "/remote/a.bin", totalSize: 4, taskId: "task-1", attemptId: "attempt-1", sha256: "not-yet-read" });
// @ts-expect-error chunked uploads require attempt identity
void call("fs_upload_begin", { sessionId: "session-1", remotePath: "/remote/a.bin", totalSize: 4, taskId: "task-1" });
// @ts-expect-error base64 content remains text rather than a number array
void call("fs_upload_chunk", { sessionId: "session-1", uploadId: "upload-1", taskId: "task-1", attemptId: "attempt-1", index: 0, size: 4, sha256: "hash", contentBase64: [0, 1, 2, 255] });
// @ts-expect-error pack download has no resume flag in its existing facade contract
void call("fs_pack_download", { sessionId: "session-1", remotePath: "/remote/dir", localPath: "C:\\local\\dir.tar.gz", resume: true });
// @ts-expect-error nested mount creation rejects flat input
void call("mount_create", { sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:" });
// @ts-expect-error mount secrets remain strings
void call("mount_create", { args: { sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:", password: 42 } });
// @ts-expect-error mount lists remain scoped to a session
void call("mount_list", { forceRefresh: true });

async function generatedOutputs() {
  const read = await call("fs_read", { sessionId: "session-1", path: "/remote/a.bin" });
  const contentBase64: string = read.contentBase64;
  const range = await call("fs_read_range", { sessionId: "session-1", path: "/remote/a.bin", offset: 0 });
  const truncated: boolean = range.truncated;
  const roots = await call("fs_roots", { sessionId: "session-1" });
  const generatedPathStyle: string = roots.pathStyle;
  const folder = await call("local_folder_list", { path: "C:\\local\\pics" });
  const nativePath: string = folder.files[0].localPath;
  const mount = await call("mount_create", { args: { sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:", password: "secret" } });
  const managed: boolean = mount.managed;
  // @ts-expect-error mount results never echo the create secret
  const password = mount.password;
  const facadeRoots = await fsApi.roots("session-1");
  const facadePathStyle: "windows" | "posix" | "unknown" = facadeRoots.pathStyle;
  void [contentBase64, truncated, generatedPathStyle, nativePath, managed, password, facadePathStyle];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types the Docker vertical without inventing inspect or stream schemas", () => {
    const probe = resolve("src/test/__ipc_docker_vertical_contract_probe.ts");
    const source = `
import {
  call,
  dockerApi,
  type ComposeOverview,
  type ContainerSummary,
  type DockerDiskUsage,
  type DockerPrunePreview,
} from "../ipc/commands";
import { ipcContractManifest, type IPCInput, type IPCOutput } from "../ipc/generated/manifest";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type ContractChecks = [
  Assert<Equal<IPCOutput<"docker_inspect">, unknown>>,
  Assert<Equal<IPCOutput<"docker_action">, unknown>>,
  Assert<Equal<IPCOutput<"docker_image_remove">, unknown>>,
  Assert<Equal<IPCOutput<"docker_compose_action">, string>>,
  Assert<Equal<IPCOutput<"docker_prune_preview">["expiresAt"], number>>,
  Assert<Equal<IPCOutput<"docker_compose_redeploy_prepare">["contentHash"], string>>,
  Assert<Equal<IPCOutput<"docker_compose_redeploy_prepare">["ticket"], string>>,
  Assert<Equal<IPCOutput<"docker_overview">["hostStats"]["containersTotal"], number>>,
  Assert<Equal<IPCInput<"docker_exec_attach">["cmd"], string | undefined>>,
  Assert<Equal<IPCInput<"docker_action">["newName"], string | undefined>>,
  Assert<Equal<IPCInput<"docker_image_remove">["force"], boolean | undefined>>,
  Assert<Equal<(typeof ipcContractManifest.commands)["docker_logs_attach"]["streamPayload"], "unknown">>,
  Assert<Equal<(typeof ipcContractManifest.commands)["docker_exec_attach"]["streamPayload"], "unknown">>,
  Assert<Equal<(typeof ipcContractManifest.commands)["docker_compose_logs_attach"]["streamPayload"], "unknown">>,
  Assert<Equal<(typeof ipcContractManifest.commands)["docker_compose_redeploy_attach"]["streamPayload"], "unknown">>,
];
const contractChecks: ContractChecks = [true, true, true, true, true, true, true, true, true, true, true, true, true, true, true];
void contractChecks;

const containers: Promise<ContainerSummary[]> = dockerApi.ps("s1");
const compose: Promise<ComposeOverview> = dockerApi.composeStacks("s1");
const disk: Promise<DockerDiskUsage> = dockerApi.diskUsage("s1");
const preview: Promise<DockerPrunePreview> = dockerApi.prunePreview("s1", ["containers"]);
const inspected: Promise<unknown> = dockerApi.inspect("s1", "c1");
const prepared: Promise<{ ticket: string; contentHash: string }> = dockerApi.composeRedeployPrepare({
  sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3",
});
const attached: Promise<string> = dockerApi.execAttach("s1", "c1", 80, 24, {});
const removed: Promise<void> = dockerApi.imageRemove("s1", "nginx:latest");
void [containers, compose, disk, preview, inspected, prepared, attached, removed];

void call("docker_ps", { sessionId: "s1" });
void call("docker_action", { args: { sessionId: "s1", containerId: "c1", action: "restart" } });
void call("docker_exec_attach", { args: { sessionId: "s1", containerId: "c1", cols: 80, rows: 24, clientId: "client-1" }, channel: {} });
void call("docker_logs_attach", { sessionId: "s1", containerId: "c1", tail: 500, channel: {} });
void call("docker_compose_action", { sessionId: "s1", project: "shop", action: "down", productionConfirmation: {
  confirmed: true, targetSetHash: "a", commandHash: "b", environmentNames: ["production"],
} });
void call("docker_compose_redeploy_prepare", {
  sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3",
  productionConfirmation: { confirmed: true, targetSetHash: "a", commandHash: "b" },
});
void call("docker_compose_redeploy_attach", { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", ticket: "ticket-1", channel: {} });
void call("docker_prune", { sessionId: "s1", token: "preview-token", digest: "c" });
void call("docker_inspect", { sessionId: "s1", containerId: "c1" });

// @ts-expect-error typed docker commands reject the legacy generic output escape
void call<ContainerSummary[]>("docker_ps", { sessionId: "s1" });
// @ts-expect-error nested container actions reject flat input
void call("docker_action", { sessionId: "s1", containerId: "c1", action: "restart" });
// @ts-expect-error exec attachment retains its nested envelope and required channel
void call("docker_exec_attach", { sessionId: "s1", containerId: "c1", cols: 80, rows: 24 });
// @ts-expect-error logs attachment requires its binary channel
void call("docker_logs_attach", { sessionId: "s1", containerId: "c1", tail: 500 });
// @ts-expect-error exec dimensions remain numeric
void call("docker_exec_attach", { args: { sessionId: "s1", containerId: "c1", cols: "80", rows: 24, clientId: "client-1" }, channel: {} });
// @ts-expect-error redeploy preparation has no attach ticket
void call("docker_compose_redeploy_prepare", { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3", ticket: "not-yet-issued" });
// @ts-expect-error redeploy attachment cannot carry a production confirmation around the prepare guard
void call("docker_compose_redeploy_attach", { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", ticket: "ticket-1", productionConfirmation: { confirmed: true, targetSetHash: "a", commandHash: "b" }, channel: {} });
// @ts-expect-error redeploy attachment requires the issued ticket
void call("docker_compose_redeploy_attach", { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", channel: {} });
// @ts-expect-error prune digest remains text
void call("docker_prune", { sessionId: "s1", token: "preview-token", digest: 42 });
// @ts-expect-error production confirmation fields remain typed
void call("docker_compose_action", { sessionId: "s1", project: "shop", action: "down", productionConfirmation: { confirmed: "yes", targetSetHash: "a", commandHash: "b" } });
// @ts-expect-error the public facade still requires an explicit true confirmation
void dockerApi.composeAction("s1", "shop", "down", { confirmed: false, targetSetHash: "a", commandHash: "b" });

async function generatedOutputs() {
  const preview = await call("docker_prune_preview", { sessionId: "s1", kinds: ["containers"] });
  const token: string = preview.token;
  const digest: string = preview.digest;
  const expiresAt: number = preview.expiresAt;
  const prepared = await call("docker_compose_redeploy_prepare", { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3" });
  const ticket: string = prepared.ticket;
  const contentHash: string = prepared.contentHash;
  const inspect: unknown = await call("docker_inspect", { sessionId: "s1", containerId: "c1" });
  // @ts-expect-error dynamic inspect JSON requires runtime narrowing
  const inspectID: string = inspect.Id;
  void [token, digest, expiresAt, ticket, contentHash, inspect, inspectID];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types database and Redis without widening query limits or dynamic JSON", () => {
    const probe = resolve("src/test/__ipc_db_redis_vertical_contract_probe.ts");
    const source = `
import { call, dbApi, type QueryResult, type TableDescribe } from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type { QueryResultDto, RedisKeyViewDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
const disconnectUnknown: Assert<Equal<IPCOutput<"db_disconnect">, unknown>> = true;
const ttlUnknown: Assert<Equal<IPCOutput<"redis_set_ttl">, unknown>> = true;
const rowsUnknown: Assert<Equal<IPCOutput<"db_query">["rows"][number][number], unknown>> = true;
const redisValueUnknown: Assert<Equal<IPCOutput<"redis_inspect">["value"], unknown>> = true;
const scanTuple: Assert<Equal<IPCOutput<"redis_scan">, [number, string[]]>> = true;
const tableSchema: Assert<Equal<IPCInput<"db_tables">["schema"], string | undefined>> = true;
const columnSchema: Assert<Equal<IPCInput<"db_columns">["schema"], string | undefined>> = true;
const queryTimeout: Assert<Equal<IPCInput<"db_query">["timeoutMs"], number | null | undefined>> = true;
const scanCursor: Assert<Equal<IPCInput<"redis_scan">["cursor"], number | null | undefined>> = true;
const scanPattern: Assert<Equal<IPCInput<"redis_scan">["pattern"], string | null | undefined>> = true;
const scanCount: Assert<Equal<IPCInput<"redis_scan">["count"], number | null | undefined>> = true;
const columnDefault: Assert<Equal<IPCOutput<"db_columns">["columns"][number]["default"], string | null>> = true;
const queryError: Assert<Equal<IPCOutput<"db_query">["error"], string | null>> = true;
const manualRowsUnknown: Assert<Equal<QueryResultDto["rows"][number][number], unknown>> = true;
const manualRedisUnknown: Assert<Equal<RedisKeyViewDto["value"], unknown>> = true;
void [disconnectUnknown, ttlUnknown, rowsUnknown, redisValueUnknown, scanTuple, tableSchema, columnSchema, queryTimeout, scanCursor, scanPattern, scanCount, columnDefault, queryError, manualRowsUnknown, manualRedisUnknown];

const connected: Promise<{ connId: string }> = dbApi.connect("asset-1");
const disconnected: Promise<void> = dbApi.disconnect("conn-1");
const schemas: Promise<string[]> = dbApi.schemas("conn-1");
const tables: Promise<string[]> = dbApi.tables("conn-1", "");
const columns: Promise<TableDescribe> = dbApi.columns("conn-1", "orders", "");
const queried: Promise<QueryResult> = dbApi.query("conn-1", "SELECT 1", 0);
const scanned: Promise<[number, string[]]> = dbApi.redisScan("conn-1", 0, "", 0);
const inspected: Promise<RedisKeyViewDto> = dbApi.redisInspect("conn-1", "");
const command: Promise<string> = dbApi.redisCommand("conn-1", ["SET", "key", ""]);
const ttl: Promise<void> = dbApi.redisSetTtl("conn-1", "", 0);
void [connected, disconnected, schemas, tables, columns, queried, scanned, inspected, command, ttl];

void call("db_connect", { args: { assetId: "" } });
void call("db_disconnect", { connId: "conn-1" });
void call("db_schemas", { connId: "conn-1" });
void call("db_tables", { connId: "conn-1", schema: "" });
void call("db_columns", { connId: "conn-1", table: "orders", schema: "" });
void call("db_query", { connId: "conn-1", sql: "SELECT 1", timeoutMs: 0 });
void call("db_query", { connId: "conn-1", sql: "SELECT 1", timeoutMs: null });
void call("redis_scan", { connId: "conn-1", cursor: 0, pattern: "", count: 0 });
void call("redis_scan", { connId: "conn-1", cursor: null, pattern: null, count: null });
void call("redis_inspect", { connId: "conn-1", key: "" });
void call("redis_command", { connId: "conn-1", args: ["SET", "key", ""] });
void call("redis_set_ttl", { connId: "conn-1", key: "", seconds: 0 });

// @ts-expect-error typed database commands reject the legacy generic output escape
void call<QueryResult>("db_query", { connId: "conn-1", sql: "SELECT 1" });
// @ts-expect-error only db_connect uses a nested envelope
void call("db_connect", { assetId: "asset-1" });
// @ts-expect-error table schema is optional but not nullable
void call("db_tables", { connId: "conn-1", schema: null });
// @ts-expect-error column schema is optional but not nullable
void call("db_columns", { connId: "conn-1", table: "orders", schema: null });
// @ts-expect-error the backend limit is deliberately absent from the consumer projection
void call("db_query", { connId: "conn-1", sql: "SELECT 1", limit: 10 });
// @ts-expect-error Redis args are a flat request field, not a nested envelope
void call("redis_command", { args: { connId: "conn-1", args: ["PING"] } });
// @ts-expect-error Redis cursor remains numeric
void call("redis_scan", { connId: "conn-1", cursor: "0" });
// @ts-expect-error the query facade does not widen its optional timeout to null
void dbApi.query("conn-1", "SELECT 1", null);
// @ts-expect-error the Redis scan facade does not widen optional fields to null
void dbApi.redisScan("conn-1", null);
// @ts-expect-error the table facade keeps schema non-nullable
void dbApi.tables("conn-1", null);

async function generatedOutputs() {
  const result = await call("db_query", { connId: "conn-1", sql: "SELECT 1" });
  const cell: unknown = result.rows[0][0];
  const truncated: boolean = result.truncated;
  const rowsAffected: number = result.rowsAffected;
  const queryError: string | null = result.error;
  // @ts-expect-error dynamic query cells require runtime narrowing
  const cellText: string = result.rows[0][0];
  const view = await call("redis_inspect", { connId: "conn-1", key: "key" });
  const value: unknown = view.value;
  // @ts-expect-error dynamic Redis JSON requires runtime narrowing
  const valueText: string = view.value;
  const [cursor, keys] = await call("redis_scan", { connId: "conn-1" });
  const nextCursor: number = cursor;
  const scannedKeys: string[] = keys;
  void [cell, truncated, rowsAffected, queryError, cellText, value, valueText, nextCursor, scannedKeys];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types the transcript vertical without widening its public facade", () => {
    const probe = resolve("src/test/__ipc_transcript_vertical_contract_probe.ts");
    const source = `
import {
  call,
  transcriptApi,
  type TranscriptChunk,
  type TranscriptHost,
  type TranscriptMatch,
  type TranscriptReadResult,
  type TranscriptSummary,
} from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
const deleteUnknown: Assert<Equal<IPCOutput<"transcript_delete">, unknown>> = true;
const optInEmpty: Assert<Equal<IPCOutput<"transcript_sync_opt_in">, Record<string, never>>> = true;
const cursorRequired: Assert<Equal<IPCInput<"transcript_read">["afterSeq"], number>> = true;
const maxBytesOptional: Assert<Equal<IPCInput<"transcript_read">["maxBytes"], number | undefined>> = true;
const generatedKind: Assert<Equal<IPCOutput<"transcript_read">["chunks"][number]["kind"], number>> = true;
const generatedStoredBytes: Assert<Equal<IPCOutput<"transcript_list">[number]["storedBytes"], number>> = true;
const publicKind: Assert<Equal<TranscriptChunk["kind"], number | undefined>> = true;
const publicStoredBytes: Assert<Equal<TranscriptSummary["storedBytes"], number | undefined>> = true;
const publicSyncOptIn: Assert<Equal<TranscriptSummary["syncOptIn"], boolean | undefined>> = true;
const publicContentOmitted: Assert<Equal<TranscriptSummary["contentOmitted"], boolean | undefined>> = true;
const facadeList: Assert<Equal<ReturnType<typeof transcriptApi.list>, Promise<TranscriptSummary[]>>> = true;
const facadeRead: Assert<Equal<ReturnType<typeof transcriptApi.read>, Promise<TranscriptReadResult>>> = true;
const facadeDelete: Assert<Equal<ReturnType<typeof transcriptApi.remove>, Promise<void>>> = true;
const facadeOptIn: Assert<Equal<ReturnType<typeof transcriptApi.syncOptIn>, Promise<void>>> = true;
void [deleteUnknown, optInEmpty, cursorRequired, maxBytesOptional, generatedKind, generatedStoredBytes, publicKind, publicStoredBytes, publicSyncOptIn, publicContentOmitted, facadeList, facadeRead, facadeDelete, facadeOptIn];

const hosts: Promise<TranscriptHost[]> = transcriptApi.hosts();
const summaries: Promise<TranscriptSummary[]> = transcriptApi.list("a1");
const read: Promise<TranscriptReadResult> = transcriptApi.read("t1");
const matches: Promise<TranscriptMatch[]> = transcriptApi.search("t1", "needle");
const removed: Promise<void> = transcriptApi.remove("t1");
const optedIn: Promise<void> = transcriptApi.syncOptIn("t1", true);
void [hosts, summaries, read, matches, removed, optedIn];
void transcriptApi.read("t1", 4, 512 * 1024);

void call("transcript_hosts");
void call("transcript_list", { assetId: "a1" });
void call("transcript_read", { id: "t1", afterSeq: 0 });
void call("transcript_read", { id: "t1", afterSeq: 4, maxBytes: 4096 });
void call("transcript_search", { id: "t1", query: "needle" });
void call("transcript_delete", { id: "t1" });
void call("transcript_sync_opt_in", { id: "t1", optIn: true });

// @ts-expect-error typed transcript commands reject the legacy generic output escape
void call<TranscriptReadResult>("transcript_read", { id: "t1", afterSeq: 0 });
// @ts-expect-error the inclusive cursor is required by the generated call contract
void call("transcript_read", { id: "t1" });
// @ts-expect-error maxBytes is optional but not nullable
void call("transcript_read", { id: "t1", afterSeq: 0, maxBytes: null });
// @ts-expect-error owner identity comes from context, not client input
void call("transcript_list", { assetId: "a1", ownerId: "other-user" });
// @ts-expect-error no-argument hosts rejects owner injection
void call("transcript_hosts", { ownerId: "other-user" });
// @ts-expect-error transcript opt-in remains flat, not nested like sync_* mutations
void call("transcript_sync_opt_in", { args: { id: "t1", optIn: true } });
// @ts-expect-error optIn remains boolean
void call("transcript_sync_opt_in", { id: "t1", optIn: "yes" });
// @ts-expect-error the public facade does not widen optional maxBytes to null
void transcriptApi.read("t1", 0, null);

async function generatedOutputs() {
  const result = await call("transcript_read", { id: "t1", afterSeq: 0 });
  const kind: number = result.chunks[0].kind;
  const nextSeq: number = result.nextSeq;
  const done: boolean = result.done;
  const totalBytes: number = result.totalBytes;
  const deleted = await call("transcript_delete", { id: "t1" });
  const dynamic: unknown = deleted;
  // @ts-expect-error delete keeps its dynamic any output as unknown
  const deletedField = deleted.field;
  const opted = await call("transcript_sync_opt_in", { id: "t1", optIn: false });
  const empty: Record<string, never> = opted;
  void [kind, nextSeq, done, totalBytes, dynamic, deletedField, empty];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types platform, update and biometric commands while preserving public facades and app_info legacy", () => {
    const probe = resolve("src/test/__ipc_platform_update_biometric_contract_probe.ts");
    const source = `
import {
  appUpdateApi,
  biometricApi,
  call,
  desktopTrayApi,
  systemApi,
  type BiometricStatus,
  type BiometricUnlockResult,
  type DesktopTrayDto,
} from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type { UpdateResultDto, UpdateStatusDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type ContractChecks = [
  Assert<Equal<IPCInput<"app_update_install">["version"], string | null>>,
  Assert<Equal<IPCOutput<"app_update_check">["unavailableReason"], string>>,
  Assert<Equal<IPCOutput<"app_restart">["restarted"], boolean>>,
  Assert<Equal<IPCOutput<"app_restart">["installed"], boolean>>,
  Assert<Equal<IPCOutput<"biometric_status">["canEvaluate"], boolean>>,
  Assert<Equal<IPCOutput<"biometric_unlock">["password"], string | undefined>>,
  Assert<Equal<IPCOutput<"biometric_enable">, unknown>>,
  Assert<Equal<IPCOutput<"biometric_disable">, unknown>>,
  Assert<Equal<IPCOutput<"system_insets">["top"], number>>,
];
const contractChecks: ContractChecks = [true, true, true, true, true, true, true, true, true];
void contractChecks;

const platform: Promise<string> = systemApi.platform();
const insets: Promise<{ top: number; right: number; bottom: number; left: number }> = systemApi.insets();
const tray: Promise<DesktopTrayDto> = desktopTrayApi.get();
const savedTray: Promise<DesktopTrayDto> = desktopTrayApi.set(false);
const status: Promise<BiometricStatus> = biometricApi.status();
const unlocked: Promise<BiometricUnlockResult> = biometricApi.unlock();
const enabled: Promise<void> = biometricApi.enable("master-password");
const disabled: Promise<void> = biometricApi.disable();
const checked: Promise<UpdateStatusDto> = appUpdateApi.check();
const installed: Promise<UpdateResultDto> = appUpdateApi.install();
const installedExplicit: Promise<UpdateResultDto> = appUpdateApi.install("1.2.3");
const installedLatest: Promise<UpdateResultDto> = appUpdateApi.install("");
const restarted: Promise<UpdateResultDto> = appUpdateApi.restart();
void [platform, insets, tray, savedTray, status, unlocked, enabled, disabled, checked, installed, installedExplicit, installedLatest, restarted];
void appUpdateApi.install(undefined);

void call("app_platform");
void call("system_insets");
void call("desktop_tray_get");
void call("desktop_tray_set", { enabled: false });
void call("app_update_check");
void call("app_update_install", { version: null });
void call("app_update_install", { version: "1.2.3" });
void call("app_update_install", { version: "" });
void call("app_restart");
void call("biometric_status");
void call("biometric_unlock");
void call("biometric_enable", { password: "master-password" });
void call("biometric_disable");
void call<unknown>("app_info");

// @ts-expect-error typed platform commands reject the legacy generic output escape
void call<string>("app_platform");
// @ts-expect-error typed update commands reject the legacy generic output escape
void call<UpdateStatusDto>("app_update_check");
// @ts-expect-error no-argument commands reject unexpected input
void call("app_restart", { unexpected: true });
// @ts-expect-error the wire version is explicitly required and nullable; omission belongs to the facade only
void call("app_update_install", {});
// @ts-expect-error update install is flat, not nested
void call("app_update_install", { args: { version: null } });
// @ts-expect-error explicit versions remain strings or null
void call("app_update_install", { version: 42 });
// @ts-expect-error tray preference remains boolean
void call("desktop_tray_set", { enabled: "yes" });
// @ts-expect-error biometric enable still requires the password
void call("biometric_enable", {});
// @ts-expect-error the facade does not widen optional versions to null
void appUpdateApi.install(null);

async function generatedOutputs() {
  const status = await call("app_update_check");
  const canInstall: boolean = status.canInstall;
  const result = await call("app_restart");
  const installed: boolean = result.installed;
  const restarted: boolean = result.restarted;
  const unavailableReason: string = result.unavailableReason;
  const unlocked = await call("biometric_unlock");
  const password: string | undefined = unlocked.password;
  // @ts-expect-error biometric availability never exposes stored material
  const material = (await call("biometric_status")).password;
  void [canInstall, installed, restarted, unavailableReason, password, material];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types the forward runtime vertical without widening risk, status or facade compatibility", () => {
    const probe = resolve("src/test/__ipc_forward_runtime_contract_probe.ts");
    const source = `
import { call, forwardApi, type ForwardSpec } from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type { ForwardEnvDto, ForwardSpecDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type ContractChecks = [
  Assert<Equal<IPCOutput<"forward_remove">, unknown>>,
  Assert<Equal<IPCOutput<"forward_env">["listenHost"], string>>,
  Assert<Equal<IPCOutput<"forward_env">["reason"], string | undefined>>,
  Assert<Equal<IPCOutput<"forward_create">["status"], string>>,
  Assert<Equal<IPCOutput<"forward_create">["listenHost"], string>>,
  Assert<Equal<IPCOutput<"forward_create">["targetHost"], string | null>>,
  Assert<Equal<IPCOutput<"forward_create">["targetPort"], number | null>>,
  Assert<Equal<IPCOutput<"forward_create">["hostId"], string | undefined>>,
  Assert<Equal<IPCOutput<"forward_create">["error"], string | undefined>>,
  Assert<Equal<IPCInput<"forward_retry">["sessionId"], string | undefined>>,
  Assert<Equal<IPCInput<"forward_create_socks">["acknowledgeRisk"], boolean | undefined>>,
  Assert<Equal<IPCInput<"forward_create_remote">["acknowledgeRisk"], boolean | undefined>>,
  Assert<Equal<IPCInput<"forward_create_remote">["bindHost"], string>>,
  Assert<Equal<IPCInput<"forward_create_remote">["bindPort"], number>>,
  Assert<Equal<IPCInput<"forward_create_remote">["targetHost"], string>>,
  Assert<Equal<IPCInput<"forward_create_remote">["targetPort"], number>>,
  Assert<Equal<ForwardSpec["status"], "active" | "reconnecting" | "failed">>,
  Assert<Equal<ForwardSpec["listenHost"], string | undefined>>,
  Assert<Equal<ForwardSpecDto["targetHost"], string | null>>,
  Assert<Equal<ForwardSpecDto["targetPort"], number | null>>,
];
const contractChecks: ContractChecks = [true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true];
void contractChecks;

const env: Promise<ForwardEnvDto> = forwardApi.env();
const created: Promise<ForwardSpec> = forwardApi.create("session-1", 13306, "db.internal", 5432);
const socks: Promise<ForwardSpec> = forwardApi.createSocks("session-1", 1080, false);
const remote: Promise<ForwardSpec> = forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1", 8080, false);
const listed: Promise<ForwardSpec[]> = forwardApi.list();
const retried: Promise<ForwardSpec> = forwardApi.retry("forward-1", "");
const removed: Promise<void> = forwardApi.remove("forward-1");
void [env, created, socks, remote, listed, retried, removed];
void forwardApi.createSocks("session-1", 1080);
void forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1", 8080);
void forwardApi.retry("forward-1");
void forwardApi.retry("forward-1", undefined);

void call("forward_env");
void call("forward_create", { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432 });
void call("forward_create_socks", { sessionId: "session-1", listenPort: 1080 });
void call("forward_create_socks", { sessionId: "session-1", listenPort: 1080, acknowledgeRisk: false });
void call("forward_create_remote", { sessionId: "session-1", bindHost: "127.0.0.1", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080 });
void call("forward_create_remote", { sessionId: "session-1", bindHost: "0.0.0.0", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080, acknowledgeRisk: true });
void call("forward_list");
void call("forward_retry", { id: "forward-1" });
void call("forward_retry", { id: "forward-1", sessionId: "" });
void call("forward_remove", { id: "forward-1" });

// @ts-expect-error typed forward commands reject the legacy generic output escape
void call<ForwardSpec>("forward_create", { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432 });
// @ts-expect-error local creation does not acquire a risk acknowledgement
void call("forward_create", { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432, acknowledgeRisk: true });
// @ts-expect-error runtime creation does not reopen saved-rule autoStart
void call("forward_create", { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432, autoStart: true });
// @ts-expect-error remote bindHost remains required despite the backend default
void call("forward_create_remote", { sessionId: "session-1", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080 });
// @ts-expect-error remote target fields remain required
void call("forward_create_remote", { sessionId: "session-1", bindHost: "127.0.0.1", bindPort: 18080, targetHost: "127.0.0.1" });
// @ts-expect-error risk acknowledgement remains boolean
void call("forward_create_socks", { sessionId: "session-1", listenPort: 1080, acknowledgeRisk: "yes" });
// @ts-expect-error retry session identity remains a string
void call("forward_retry", { id: "forward-1", sessionId: 42 });
// @ts-expect-error forward runtime commands remain flat
void call("forward_retry", { args: { id: "forward-1" } });
// @ts-expect-error no-argument list rejects owner or rule controls
void call("forward_list", { autoStart: true });
// @ts-expect-error the public runtime facade does not acquire saved-rule stopped status
const stopped: ForwardSpec["status"] = "stopped";
void stopped;
// @ts-expect-error the facade keeps all remote bind and target arguments required
void forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1");

async function generatedOutputs() {
  const generated = await call("forward_create", { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432 });
  const generatedStatus: string = generated.status;
  const generatedListenHost: string = generated.listenHost;
  const generatedTargetHost: string | null = generated.targetHost;
  const generatedTargetPort: number | null = generated.targetPort;
  const generatedHostId: string | undefined = generated.hostId;
  const generatedError: string | undefined = generated.error;
  // @ts-expect-error exposure risk remains needs_confirm error detail, not a normal output field
  const risk = generated.risk;
  const generatedEnv = await call("forward_env");
  const generatedReason: string | undefined = generatedEnv.reason;
  const facadeEnv = await forwardApi.env();
  // @ts-expect-error the existing public env DTO does not expose the backend reason field
  const facadeReason = facadeEnv.reason;
  const facade = await forwardApi.retry("forward-1");
  const facadeStatus: "active" | "reconnecting" | "failed" = facade.status;
  const facadeListenHost: string | undefined = facade.listenHost;
  const removeResult: unknown = await call("forward_remove", { id: "forward-1" });
  void [generatedStatus, generatedListenHost, generatedTargetHost, generatedTargetPort, generatedHostId, generatedError, risk, generatedReason, facadeReason, facadeStatus, facadeListenHost, removeResult];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types the consumer-priority session terminal vertical without narrowing legacy fallbacks", () => {
    const probe = resolve("src/test/__ipc_session_terminal_contract_probe.ts");
    const source = `
import { call, sessionApi, terminalApi } from "../ipc/commands";
import type { AttachedTabInfo, DurableDescriptor, LiveTabInfo, SessionInfo } from "../ipc/commands";
import { ipcContractManifest, type IPCOutput } from "../ipc/generated/manifest";
import type { ScreenSnapshotDto } from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type DisconnectUnknown = Assert<Equal<IPCOutput<"session_disconnect">, unknown>>;
type WriteUnknown = Assert<Equal<IPCOutput<"terminal_write">, unknown>>;
type AttachStreamUnknown = Assert<Equal<(typeof ipcContractManifest.commands)["terminal_attach"]["streamPayload"], "unknown">>;
const outputChecks: [DisconnectUnknown, WriteUnknown, AttachStreamUnknown] = [true, true, true];
void outputChecks;

const sessions: Promise<SessionInfo[]> = sessionApi.list();
const attached: Promise<AttachedTabInfo> = terminalApi.attach("session-1", 80, 24, {});
const disconnected: Promise<void> = sessionApi.disconnect("session-1");
const prepared: Promise<void> = terminalApi.prepareBroadcast(["tab-1"], new Uint8Array([1]));
const written: Promise<void> = terminalApi.write("tab-1", new Uint8Array([1]));
const resized: Promise<void> = terminalApi.resize("tab-1", 80, 24);
const live: Promise<LiveTabInfo[]> = terminalApi.listLive();
const durable: Promise<DurableDescriptor[]> = terminalApi.listDurable("session-1");
const helperOperation = terminalApi.helperOperation("session-1", "operation-1", "cancel");
const helperOffline = terminalApi.helperOffline("session-1", "/tmp/helper", "a".repeat(64), true);
void [sessions, attached, disconnected, prepared, written, resized, live, durable, helperOperation, helperOffline];

void call("session_connect_device", { args: { deviceId: "device-1", name: "Device" } });
void call("session_connect_local");
void call("session_disconnect", { sessionId: "session-1" });
void call("session_list");
void call("session_open_line_tab", { sessionId: "session-1", cols: 120, rows: 40, channel: {} });
void call("session_quick_connect_user");
void call("session_reconnect", { sessionId: "session-1" });
void call("terminal_attach", { sessionId: "session-1", cols: 80, rows: 24, durable: false, channel: {} });
void call("terminal_broadcast_prepare", { args: { tabIds: ["tab-1"], data: [1] } });
void call("terminal_rename_tab", { tabId: "tab-1", title: "Deploy" });
void call("terminal_write", { args: { tabId: "tab-1", clientId: "client-1", data: [1] } });
void call("terminal_helper_operation", { sessionId: "session-1", operationId: "operation-1", action: "cancel" });
void call("terminal_helper_offline", { sessionId: "session-1", path: "/tmp/helper", sha256: "a".repeat(64), confirmed: true });

// @ts-expect-error generated helper operation input fields are checked
void call("terminal_helper_operation", { sessionId: "session-1", operationId: 42, action: "cancel" });

// @ts-expect-error generated device input fields are checked
void call("session_connect_device", { args: { deviceId: 42, name: "Device" } });
// @ts-expect-error selected attach commands require their existing channel
void call("terminal_attach", { sessionId: "session-1", cols: 80, rows: 24 });
// @ts-expect-error selected nested write commands reject flat input
void call("terminal_write", { tabId: "tab-1", clientId: "client-1", data: [1] });

void call<SessionInfo>("session_connect", { args: { assetId: "asset-1" } });
void call<AttachedTabInfo>("terminal_attach_tab", { tabId: "tab-1", replayBytes: undefined, channel: {} });
void call<void>("terminal_resize", { tabId: "tab-1", clientId: "client-1", cols: 80, rows: 24 });
void call<void>("terminal_resize_flush", { tabId: "tab-1", clientId: "client-1" });
void call<ScreenSnapshotDto>("terminal_snapshot", { tabId: "tab-1" });
void call<LiveTabInfo[]>("terminal_list");
void call<DurableDescriptor[]>("terminal_list", { sessionId: "session-1", durable: true });

async function generatedOutputs() {
  const listed = await call("session_list");
  const staleConfig: boolean = listed[0].staleConfig;
  const nextRetryAt: string | null | undefined = listed[0].nextRetryAt;
  const attached = await call("terminal_attach", { sessionId: "session-1", cols: 80, rows: 24, channel: {} });
  const retainable: boolean = attached.retainable;
  // @ts-expect-error generated output fields are checked
  const invalidTabID: number = attached.tabId;
  void [staleConfig, nextRetryAt, retainable, invalidTabID];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);

  it("types operations consumers while preserving optional patches, dynamic data and narrow facades", () => {
    const probe = resolve("src/test/__ipc_operations_contract_probe.ts");
    const source = `
import {
  assetApi,
  call,
  layoutApi,
  sshImportApi,
  type LayoutDto,
  type LayoutSaveResult,
} from "../ipc/commands";
import type { IPCInput, IPCOutput } from "../ipc/generated/manifest";
import type {
  AuditCountDto,
  AuditEntryDto,
  CommandEntryDto,
  JsonValue,
  SnippetDto,
  SshImportAction,
  SshImportApplyDto,
  SshImportPreviewDto,
} from "../ipc/types";

type Equal<Left, Right> =
  (<T>() => T extends Left ? 1 : 2) extends
  (<T>() => T extends Right ? 1 : 2) ? true : false;
type Assert<Value extends true> = Value;
type ContractChecks = [
  Assert<Equal<IPCOutput<"snippet_create">, Record<string, string>>>,
  Assert<Equal<IPCOutput<"snippet_update">, unknown>>,
  Assert<Equal<IPCOutput<"snippet_delete">, unknown>>,
  Assert<Equal<IPCInput<"snippet_create">["groupId"], string | null | undefined>>,
  Assert<Equal<IPCInput<"snippet_create">["sort"], number | undefined>>,
  Assert<Equal<IPCInput<"snippet_update">["groupId"], string | null | undefined>>,
  Assert<Equal<IPCInput<"snippet_update">["sort"], number | null | undefined>>,
  Assert<Equal<IPCOutput<"audit_query">[number]["payload"], unknown>>,
  Assert<Equal<IPCOutput<"layout_get">["data"], unknown>>,
  Assert<Equal<IPCInput<"layout_put">["data"], string>>,
  Assert<Equal<IPCInput<"layout_put">["revision"], number>>,
  Assert<Equal<IPCInput<"ssh_import_apply">["groupId"], string | null | undefined>>,
  Assert<Equal<IPCInput<"ssh_import_apply">["tags"], string | null | undefined>>,
  Assert<Equal<IPCOutput<"ssh_import_preview">["hosts"][number]["action"], string>>,
  Assert<Equal<AuditEntryDto["payload"], JsonValue>>,
  Assert<Equal<SshImportPreviewDto["hosts"][number]["action"], SshImportAction>>,
  Assert<Equal<ReturnType<typeof assetApi.snippetList>, Promise<SnippetDto[]>>>,
  Assert<Equal<ReturnType<typeof assetApi.snippetCreate>, Promise<{ id: string }>>>,
  Assert<Equal<ReturnType<typeof assetApi.snippetUpdate>, Promise<void>>>,
  Assert<Equal<ReturnType<typeof assetApi.snippetDelete>, Promise<void>>>,
  Assert<Equal<ReturnType<typeof assetApi.auditQuery>, Promise<AuditEntryDto[]>>>,
  Assert<Equal<ReturnType<typeof assetApi.auditCount>, Promise<AuditCountDto>>>,
  Assert<Equal<ReturnType<typeof assetApi.commandQuery>, Promise<CommandEntryDto[]>>>,
  Assert<Equal<ReturnType<typeof assetApi.commandCount>, Promise<AuditCountDto>>>,
  Assert<Equal<ReturnType<typeof layoutApi.get>, Promise<LayoutDto>>>,
  Assert<Equal<ReturnType<typeof layoutApi.put>, Promise<LayoutSaveResult>>>,
  Assert<Equal<ReturnType<typeof sshImportApi.preview>, Promise<SshImportPreviewDto>>>,
  Assert<Equal<ReturnType<typeof sshImportApi.apply>, Promise<SshImportApplyDto>>>,
];
const contractChecks: ContractChecks = [true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true];
void contractChecks;

const snippets: Promise<SnippetDto[]> = assetApi.snippetList();
const created: Promise<{ id: string }> = assetApi.snippetCreate("Logs", "tail -f app.log");
const updated: Promise<void> = assetApi.snippetUpdate("snippet-1", "Logs", "tail -f app.log", { groupId: null, sort: 0 });
const removed: Promise<void> = assetApi.snippetDelete("snippet-1");
const audited: Promise<AuditEntryDto[]> = assetApi.auditQuery({ source: "user", limit: 100, offset: 0 });
const auditTotal: Promise<AuditCountDto> = assetApi.auditCount({ source: "user" });
const commands: Promise<CommandEntryDto[]> = assetApi.commandQuery({ assetId: "asset-1", userId: "user-1", sessionId: "session-1", limit: 100, offset: 0 });
const commandTotal: Promise<AuditCountDto> = assetApi.commandCount({ assetId: "asset-1", userId: "user-1", sessionId: "session-1" });
const layout: Promise<LayoutDto> = layoutApi.get();
const saved: Promise<LayoutSaveResult> = layoutApi.put(JSON.stringify({ v: 3, workspaces: [] }), 7);
const preview: Promise<SshImportPreviewDto> = sshImportApi.preview({ source: "ssh-home" });
const applied: Promise<SshImportApplyDto> = sshImportApi.apply({
  source: "ssh-config",
  path: "/home/demo/.ssh/config",
  groupId: "group-1",
  tags: "prod, 欧洲",
  hosts: [{ id: "host-1", action: "import" }],
  keys: [{ id: "key-1", action: "skip" }],
});
void [snippets, created, updated, removed, audited, auditTotal, commands, commandTotal, layout, saved, preview, applied];

void call("snippet_list");
void call("snippet_create", { name: "Logs", body: "tail -f app.log" });
void call("snippet_create", { name: "Logs", body: "tail -f app.log", groupId: null, sort: 0 });
void call("snippet_update", { id: "snippet-1", name: "Logs", body: "tail -f app.log" });
void call("snippet_update", { id: "snippet-1", name: "Logs", body: "tail -f app.log", groupId: null, sort: 0 });
void call("snippet_delete", { id: "snippet-1" });
void call("audit_query", { args: { sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect", limit: 100, offset: 0 } });
void call("audit_count", { args: { source: "user", kind: "connect" } });
void call("command_query", { args: { sessionId: "session-1", assetId: "asset-1", userId: "user-1", limit: 100, offset: 0 } });
void call("command_count", { args: { sessionId: "session-1", assetId: "asset-1", userId: "user-1" } });
void call("layout_get");
void call("layout_put", { args: { data: "{\\"v\\":3,\\"workspaces\\":[]}", revision: 7 } });
void call("ssh_import_preview", { args: { source: "ssh-home" } });
void call("ssh_import_apply", { args: { source: "ssh-config", groupId: "group-1", tags: "prod", hosts: [], keys: [] } });
void call("ssh_import_apply", { args: { source: "ssh-config", groupId: null, tags: null, hosts: [], keys: [] } });

// @ts-expect-error typed snippet commands reject the legacy generic output escape
void call<SnippetDto[]>("snippet_list");
// @ts-expect-error shared idRequest fields do not leak into snippet deletion
void call("snippet_delete", { id: "snippet-1", q: "unrelated" });
// @ts-expect-error snippet sort remains numeric
void call("snippet_create", { name: "Logs", body: "tail -f app.log", sort: "first" });
// @ts-expect-error count projects filters only, not pagination
void call("audit_count", { args: { source: "user", limit: 100 } });
// @ts-expect-error count projects filters only, not pagination
void call("command_count", { args: { userId: "user-1", offset: 100 } });
// @ts-expect-error audit pagination remains numeric
void call("audit_query", { args: { source: "user", limit: "100" } });
// @ts-expect-error command user filters remain strings
void call("command_query", { args: { userId: 42 } });
// @ts-expect-error nested audit commands reject flat input
void call("audit_query", { source: "user" });
// @ts-expect-error layout data remains its serialized JSON string
void call("layout_put", { args: { data: { v: 3 }, revision: 7 } });
// @ts-expect-error layout put remains nested
void call("layout_put", { data: "{}", revision: 7 });
// @ts-expect-error layout revision remains required
void call("layout_put", { args: { data: "{}" } });
// @ts-expect-error the public preview facade keeps its supported source union
void sshImportApi.preview({ source: "other" });
// @ts-expect-error the public apply facade keeps import actions narrow
void sshImportApi.apply({ source: "ssh-home", hosts: [{ id: "host-1", action: "add" }], keys: [] });
// @ts-expect-error SSH apply still requires host selections
void sshImportApi.apply({ source: "ssh-home", keys: [] });

async function generatedOutputs() {
  const audit = await call("audit_query", { args: { source: "user" } });
  const payload: unknown = audit[0].payload;
  // @ts-expect-error dynamic audit JSON requires runtime narrowing
  const payloadText: string = audit[0].payload;
  const layout = await call("layout_get");
  const data: unknown = layout.data;
  // @ts-expect-error dynamic layout JSON requires runtime narrowing
  const layoutText: string = layout.data;
  const conflict = await call("layout_put", { args: { data: "{}", revision: 7 } });
  const saved: boolean = conflict.saved;
  const conflicted: boolean = conflict.conflict;
  const created = await call("snippet_create", { name: "Logs", body: "tail -f app.log" });
  const id: string = created.id;
  void [payload, payloadText, data, layoutText, saved, conflicted, id];
}
void generatedOutputs;
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  }, COMPILE_BUDGET_MS);
});
