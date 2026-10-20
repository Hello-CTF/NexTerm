import * as ts from "typescript";
import { describe, expect, it } from "vitest";

const root = ts.sys.getCurrentDirectory();
const resolve = ts.sys.resolvePath;
const configPath = resolve("tsconfig.json");
const config = ts.readConfigFile(configPath, ts.sys.readFile);
if (config.error) throw new Error(ts.flattenDiagnosticMessageText(config.error.messageText, "\n"));
const parsed = ts.parseJsonConfigFileContent(config.config, ts.sys, root);

function compile(rootNames: string[], overlays: Record<string, string> = {}): readonly ts.Diagnostic[] {
  const normalizedOverlays = new Map(
    Object.entries(overlays).map(([fileName, source]) => [resolve(fileName), source]),
  );
  const host = ts.createCompilerHost(parsed.options);
  const readFile = host.readFile.bind(host);
  const fileExists = host.fileExists.bind(host);
  host.readFile = (fileName) => normalizedOverlays.get(resolve(fileName)) ?? readFile(fileName);
  host.fileExists = (fileName) => normalizedOverlays.has(resolve(fileName)) || fileExists(fileName);
  host.getSourceFile = (fileName, languageVersion) => {
    const source = host.readFile(fileName);
    return source === undefined ? undefined : ts.createSourceFile(fileName, source, languageVersion, true);
  };
  const program = ts.createProgram({ rootNames, options: parsed.options, host });
  return ts.getPreEmitDiagnostics(program);
}

function formatted(diagnostics: readonly ts.Diagnostic[]): string {
  return ts.formatDiagnostics(diagnostics, {
    getCanonicalFileName: (fileName) => fileName,
    getCurrentDirectory: () => root,
    getNewLine: () => "\n",
  });
}

describe("typed IPC call contract", () => {
  it("rejects unknown, dynamic and incorrectly shaped calls while preserving legacy and unsafe paths", () => {
    const probe = resolve("src/test/__ipc_typed_contract_probe.ts");
    const source = `
import { call, unsafeCall } from "../ipc/commands";
const dynamic: string = "exec_batch_execute";
// @ts-expect-error unknown commands are rejected
void call("definitely_unknown");
// @ts-expect-error dynamic strings require the explicit escape hatch
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
void unsafeCall(dynamic);
`;
    const diagnostics = compile([probe], { [probe]: source });
    expect(diagnostics, formatted(diagnostics)).toEqual([]);
  });

  it("surfaces an in-memory generated DTO rename in the real exec batch caller", () => {
    const consumer = resolve("src/ipc/execBatchApi.ts");
    const generated = resolve("src/ipc/generated/types.ts");
    const baseline = compile([consumer]);
    expect(baseline, formatted(baseline)).toEqual([]);

    const source = ts.sys.readFile(generated);
    if (source === undefined) throw new Error("generated IPC types are missing");
    const diagnostics = compile([consumer], { [generated]: source.replaceAll("batchId", "batchID") });
    expect(diagnostics.length).toBeGreaterThan(0);
    expect(diagnostics.some((diagnostic) => diagnostic.file?.fileName === consumer)).toBe(true);
  });
});
