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
void call<string>("app_platform");
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
void [sessions, attached, disconnected, prepared, written, resized, live, durable];

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
});
