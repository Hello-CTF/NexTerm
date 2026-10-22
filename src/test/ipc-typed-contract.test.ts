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
void call<{ connect: string }>("sync_link_set", { args: {} });
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
});
