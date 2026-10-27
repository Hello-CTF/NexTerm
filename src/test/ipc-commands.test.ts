import { afterEach, beforeEach, describe, expect, expectTypeOf, it, vi } from "vitest";
import type { DockerProductionConfirmation } from "../ipc/commands";
import type { DockerProductionConfirmation as GeneratedDockerProductionConfirmation } from "../ipc/generated/types";
import { createWailsMock, type WailsMock } from "./wailsMock";

let runtime: WailsMock;

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
});

function requestAt(index = 0): Record<string, unknown> {
  return runtime.Call.ByName.mock.calls[index][1] as Record<string, unknown>;
}

describe("desktop command envelopes", () => {
  it("aligns the Docker confirmation facade with the shared DTO without widening confirmed", () => {
    expectTypeOf<Omit<DockerProductionConfirmation, "confirmed">>().toEqualTypeOf<
      Omit<GeneratedDockerProductionConfirmation, "confirmed">
    >();
    expectTypeOf<DockerProductionConfirmation["confirmed"]>().toEqualTypeOf<true>();
    if (false) {
      const rejected: DockerProductionConfirmation = {
        // @ts-expect-error the facade requires explicit production confirmation
        confirmed: false,
        targetSetHash: "a".repeat(64),
        commandHash: "b".repeat(64),
      };
      void rejected;
    }
  });

  it("preserves the full Docker confirmation in compose, redeploy, and prune envelopes", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { dockerApi } = await import("../ipc/commands");
    const productionConfirmation = {
      confirmed: true,
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      environmentNames: ["production", "staging"],
    } satisfies DockerProductionConfirmation;

    await dockerApi.composeAction("s1", "shop", "down", productionConfirmation);
    await dockerApi.composeRedeployPrepare({
      sessionId: "s1",
      project: "shop",
      path: "/srv/shop/compose.yml",
      baseContentBase64: "b2xk",
      contentBase64: "bmV3",
      productionConfirmation,
    });
    await dockerApi.prune("s1", "preview-token", "c".repeat(64), productionConfirmation);

    expect(requestAt(0)).toEqual({
      cmd: "docker_compose_action",
      args: { sessionId: "s1", project: "shop", action: "down", productionConfirmation },
    });
    expect(requestAt(1)).toEqual({
      cmd: "docker_compose_redeploy_prepare",
      args: {
        sessionId: "s1",
        project: "shop",
        path: "/srv/shop/compose.yml",
        baseContentBase64: "b2xk",
        contentBase64: "bmV3",
        productionConfirmation,
      },
    });
    expect(requestAt(2)).toEqual({
      cmd: "docker_prune",
      args: {
        sessionId: "s1",
        token: "preview-token",
        digest: "c".repeat(64),
        productionConfirmation,
      },
    });
  });

  it("sends args:null for commands without parameters", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: "macos" });
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).resolves.toBe("macos");
    expect(runtime.Call.ByName).toHaveBeenCalledWith("main.Service.Call", {
      cmd: "app_platform",
      args: null,
    });
  });

  it("promotes channel/clientId while preserving flat parameters", async () => {
    runtime.Call.ByName.mockResolvedValue({
      ok: true,
      data: { tabId: "tab-1", sessionId: "session-1", cols: 120, rows: 40, shell: "powershell" },
    });
    const { terminalApi } = await import("../ipc/commands");
    const channel = { toJSON: () => "pty-1" };

    await expect(terminalApi.attach("session-1", 120, 40, channel)).resolves.toMatchObject({
      tabId: "tab-1",
      shell: "powershell",
    });
    const request = requestAt();
    expect(JSON.parse(JSON.stringify(request))).toEqual({
      cmd: "terminal_attach",
      args: { sessionId: "session-1", cols: 120, rows: 40 },
      channel: "pty-1",
      clientId: expect.any(String),
    });
  });

  it("passes an optional host session when attaching a discovered durable tab", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: { tabId: "tab-remote", sessionId: "host-b" } });
    const { terminalApi } = await import("../ipc/commands");
    const channel = { toJSON: () => "pty-remote" };

    await terminalApi.attachTab("tab-remote", channel, 1024, undefined, "host-b");
    expect(JSON.parse(JSON.stringify(requestAt()))).toEqual({
      cmd: "terminal_attach_tab",
      args: { tabId: "tab-remote", replayBytes: 1024, sessionId: "host-b" },
      channel: "pty-remote",
      clientId: expect.any(String),
    });
  });

  it("passes the host connection when closing a discovered remote terminal", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { terminalApi } = await import("../ipc/commands");

    await terminalApi.closeTab("remote-shell", "kill", "host-b");
    expect(requestAt()).toMatchObject({
      cmd: "terminal_close_tab",
      args: { tabId: "remote-shell", mode: "kill", sessionId: "host-b" },
    });
  });

  it("preserves helper operation, update and offline trust envelopes", async () => {
    runtime.Call.ByName.mockResolvedValue({
      ok: true,
      data: {
        sessionId: "host-b",
        action: "cancel",
        accepted: true,
        local: { known: false },
        running: { known: false },
        compatibility: "unknown",
      },
    });
    const { terminalApi } = await import("../ipc/commands");

    await terminalApi.helperOperation("host-b", "operation-1", "cancel");
    await terminalApi.helperUpdate("host-b", true);
    await terminalApi.helperOffline("host-b", "/tmp/helper", "a".repeat(64), true);

    expect(requestAt(0)).toEqual({
      cmd: "terminal_helper_operation",
      args: { sessionId: "host-b", operationId: "operation-1", action: "cancel", interruptActive: false },
    });
    expect(requestAt(1)).toEqual({
      cmd: "terminal_helper_operation",
      args: { sessionId: "host-b", action: "update", interruptActive: true },
    });
    expect(requestAt(2)).toEqual({
      cmd: "terminal_helper_offline",
      args: { sessionId: "host-b", path: "/tmp/helper", sha256: "a".repeat(64), confirmed: true },
    });
  });

  it("preserves the server's current helper status", async () => {
    runtime.Call.ByName.mockResolvedValueOnce({ ok: true, data: {
      sessionId: "host-a", action: "status", accepted: false,
      local: { known: true }, running: { known: true }, compatibility: "compatible", update: { status: "current" },
    } });
    const { terminalApi } = await import("../ipc/commands");
    expect((await terminalApi.helperOperation("host-a", "", "status")).update?.status).toBe("current");
  });

  it("uses the same stable client identity when opening and executing a line tab", async () => {
    runtime.Call.ByName.mockResolvedValueOnce({ ok: true, data: "tab-1" });
    runtime.Call.ByName.mockResolvedValueOnce({ ok: true, data: null });
    const { sessionApi } = await import("../ipc/commands");
    const channel = { toJSON: () => "line-1" };

    await sessionApi.openLineTab("session-1", 120, 40, channel);
    await sessionApi.lineExec("tab-1", "Set-Location -LiteralPath 'D:/Project'");
    const openRequest = JSON.parse(JSON.stringify(requestAt(0))) as Record<string, unknown>;
    const execRequest = JSON.parse(JSON.stringify(requestAt(1))) as Record<string, unknown>;

    expect(openRequest).toEqual({
      cmd: "session_open_line_tab",
      args: { sessionId: "session-1", cols: 120, rows: 40 },
      channel: "line-1",
      clientId: expect.any(String),
    });
    expect(execRequest).toEqual({
      cmd: "session_line_exec",
      args: { tabId: "tab-1", line: "Set-Location -LiteralPath 'D:/Project'" },
      clientId: openRequest.clientId,
    });
  });

  it("does not promote or flatten clientId inside nested args", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { terminalApi } = await import("../ipc/commands");

    await terminalApi.write("tab-1", new Uint8Array([0, 127, 255]));
    expect(requestAt()).toEqual({
      cmd: "terminal_write",
      args: {
        args: { tabId: "tab-1", data: [0, 127, 255], clientId: expect.any(String) },
      },
    });
  });

  it("sends a prompt tap with the identity of the prompt the user saw", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { terminalApi } = await import("../ipc/commands");

    await terminalApi.tapPrompt("tab-1", { generation: "gen-1", epoch: 7, fingerprint: "abc" }, 1);
    expect(requestAt()).toEqual({
      cmd: "terminal_prompt_tap",
      args: { tabId: "tab-1", generation: "gen-1", epoch: 7, fingerprint: "abc", option: 1 },
      clientId: expect.any(String),
    });
  });

  it("keeps broadcast preflight and confirmation inside nested terminal args", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { terminalApi } = await import("../ipc/commands");
    const productionConfirmation = {
      confirmed: true,
      targetSetHash: "a".repeat(64),
      inputHash: "b".repeat(64),
    };

    await terminalApi.prepareBroadcast(["tab-1", "tab-2"], new Uint8Array([100, 111]), productionConfirmation);
    await terminalApi.write("tab-1", new Uint8Array([100, 111]), {
      targetTabIds: ["tab-1", "tab-2"],
      productionConfirmation,
    });

    expect(requestAt(0)).toEqual({
      cmd: "terminal_broadcast_prepare",
      args: {
        args: { tabIds: ["tab-1", "tab-2"], data: [100, 111], productionConfirmation },
      },
    });
    expect(requestAt(1)).toEqual({
      cmd: "terminal_write",
      args: {
        args: {
          tabId: "tab-1",
          data: [100, 111],
          clientId: expect.any(String),
          broadcast: { targetTabIds: ["tab-1", "tab-2"], productionConfirmation },
        },
      },
    });
  });

  it("uses flat parameters for rename and create-terminal commands", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: { tabId: "tab-1", title: "构建机" } });
    const { terminalApi } = await import("../ipc/commands");

    await expect(terminalApi.renameTab("tab-1", "构建机")).resolves.toEqual({
      tabId: "tab-1",
      title: "构建机",
    });
    expect(requestAt(0)).toEqual({
      cmd: "terminal_rename_tab",
      args: { tabId: "tab-1", title: "构建机" },
    });

    await terminalApi.createTab("session-1", { title: "Deploy", command: "make" });
    expect(requestAt(1)).toEqual({
      cmd: "terminal_create_tab",
      args: { sessionId: "session-1", title: "Deploy", command: "make" },
    });
  });

  it("queries recording status while keeping stop as a number", async () => {
    const status = {
      tabId: "tab-1",
      state: "failed",
      bytes: 23,
      path: "/tmp/recording.log",
      safeError: "disk full",
      generation: 4,
    };
    runtime.Call.ByName
      .mockResolvedValueOnce({ ok: true, data: status })
      .mockResolvedValueOnce({ ok: true, data: 23 });
    const { terminalApi } = await import("../ipc/commands");

    await expect(terminalApi.recordingStatus("tab-1")).resolves.toEqual(status);
    expect(requestAt(0)).toEqual({ cmd: "terminal_record_status", args: { tabId: "tab-1" } });
    await expect(terminalApi.recordStop("tab-1")).resolves.toBe(23);
    expect(requestAt(1)).toEqual({ cmd: "terminal_record_stop", args: { tabId: "tab-1" } });
  });

  it("uses nested args for SSH diagnostic commands", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { sessionApi, assetApi } = await import("../ipc/commands");

    await sessionApi.probeHostKey("asset-1", 5000);
    expect(requestAt(0)).toEqual({
      cmd: "session_probe_host_key",
      args: { args: { assetId: "asset-1", timeoutMs: 5000 } },
    });
    await assetApi.probeBatch(["asset-1", "asset-2"], 3000, 4);
    expect(requestAt(1)).toEqual({
      cmd: "asset_probe_batch",
      args: {
        args: { assetIds: ["asset-1", "asset-2"], timeoutMs: 3000, maxConcurrent: 4 },
      },
    });
  });

  it("carries known-host accept material only when the presented key is provided", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { assetApi } = await import("../ipc/commands");

    await assetApi.knownHostAccept("10.0.0.8", 22, "ssh-ed25519", "SHA256:abc");
    expect(requestAt(0)).toEqual({
      cmd: "known_host_accept",
      args: { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc" },
    });
    await assetApi.knownHostAccept("10.0.0.8", 22, "ssh-ed25519", "SHA256:abc", "ZmFrZS1rZXk=");
    expect(requestAt(1)).toEqual({
      cmd: "known_host_accept",
      args: { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc", key: "ZmFrZS1rZXk=" },
    });
  });

  it("keeps the takeover payload flat and channel at the top level", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: { jobId: "job-1", token: "token-1" } });
    const { aiApi } = await import("../ipc/commands");

    await aiApi.takeoverRun({
      tabId: "tab-1",
      token: "token-1",
      instruction: "Check load",
      allowWrite: false,
      channel: { toJSON: () => "ai-1" },
    });
    expect(JSON.parse(JSON.stringify(requestAt()))).toEqual({
      cmd: "ai_takeover_run",
      args: { tabId: "tab-1", token: "token-1", instruction: "Check load", allowWrite: false },
      channel: "ai-1",
    });
  });

  it("dispatches takeover controls and resumes the same job with a top-level channel", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: { jobId: "job-1", token: "token-1" } });
    const { aiApi } = await import("../ipc/commands");
    const confirmation = { jobId: "job-1", callId: "call-1", nonce: "nonce-1", decision: "allow" as const };

    await aiApi.takeoverConfirm(confirmation);
    expect(requestAt(0)).toEqual({ cmd: "ai_takeover_confirm", args: confirmation });
    await aiApi.takeoverCancel("job-1");
    expect(requestAt(1)).toEqual({ cmd: "ai_takeover_cancel", args: { jobId: "job-1" } });
    await aiApi.takeoverPause("tab-1", "token-1");
    expect(requestAt(2)).toEqual({ cmd: "ai_takeover_pause", args: { tabId: "tab-1", token: "token-1" } });
    await expect(
      aiApi.takeoverResume({
        tabId: "tab-1",
        token: "token-1",
        jobId: "job-1",
        channel: { toJSON: () => "ai-resume-1" },
      }),
    ).resolves.toEqual({ jobId: "job-1", token: "token-1" });
    expect(JSON.parse(JSON.stringify(requestAt(3)))).toEqual({
      cmd: "ai_takeover_resume",
      args: { tabId: "tab-1", token: "token-1", jobId: "job-1" },
      channel: "ai-resume-1",
    });
  });

  it("uses the new wire contract for AI confirm, answer, and takeover tokens", async () => {
    runtime.Call.ByName.mockImplementation(async (_name, request) => ({
      ok: true,
      data: (request as { cmd?: string }).cmd === "ai_takeover_enter" ? { token: "token-1" } : null,
    }));
    const { aiApi } = await import("../ipc/commands");

    await aiApi.confirm({ jobId: "job", callId: "call", nonce: "nonce", decision: "allow" });
    expect(requestAt(0)).toEqual({
      cmd: "ai_confirm",
      args: { jobId: "job", callId: "call", nonce: "nonce", decision: "allow" },
    });
    await aiApi.answer({ jobId: "job", callId: "call", nonce: "nonce", text: "Continue" });
    expect(requestAt(1)).toEqual({
      cmd: "ai_answer",
      args: { jobId: "job", callId: "call", nonce: "nonce", text: "Continue" },
    });
    await expect(aiApi.takeoverEnter("tab-1")).resolves.toEqual({ token: "token-1" });
    expect(requestAt(2)).toEqual({ cmd: "ai_takeover_enter", args: { tabId: "tab-1" } });
    await aiApi.takeoverExit("tab-1", "token-1", "done");
    expect(requestAt(3)).toEqual({
      cmd: "ai_takeover_exit",
      args: { tabId: "tab-1", token: "token-1", reason: "done" },
    });
  });

  it("uses the unified contract for model profile refresh and connectivity tests", async () => {
    runtime.Call.ByName.mockImplementation(async (_name, request) => {
      const cmd = (request as { cmd?: string }).cmd;
      if (cmd === "ai_model_refresh") {
        return { ok: true, data: { models: ["m1"], malformed: 2 } };
      }
      return { ok: true, data: { modelsOk: true, modelsError: null, chatOk: true, chatError: null } };
    });
    const { aiApi, modelApi } = await import("../ipc/commands");
    const profile = {
      id: "p1",
      name: "p1",
      baseUrl: "https://example.test",
      apiKey: "k",
      model: "m",
      temperature: 0.3,
      contextWindow: 32768,
      proxy: null,
      stream: true,
      requestTimeoutSeconds: 30,
      idleTimeoutSeconds: 0,
    };

    await expect(modelApi.refresh(profile)).resolves.toEqual({ models: ["m1"], malformed: 2 });
    expect(requestAt(0)).toEqual({ cmd: "ai_model_refresh", args: { profile } });
    await expect(modelApi.test("p1")).resolves.toEqual({
      modelsOk: true,
      modelsError: null,
      chatOk: true,
      chatError: null,
    });
    expect(requestAt(1)).toEqual({ cmd: "ai_test_provider", args: { id: "p1" } });
    await aiApi.testProvider();
    expect(requestAt(2)).toEqual({ cmd: "ai_test_provider", args: null });
  });

  it("passes connectivity test error strings through unchanged", async () => {
    runtime.Call.ByName.mockResolvedValue({
      ok: true,
      data: { modelsOk: false, modelsError: "HTTP 401: bad key", chatOk: false, chatError: "HTTP 401: bad key" },
    });
    const { modelApi } = await import("../ipc/commands");

    await expect(modelApi.test("p1")).resolves.toEqual({
      modelsOk: false,
      modelsError: "HTTP 401: bad key",
      chatOk: false,
      chatError: "HTTP 401: bad key",
    });
  });

  it("preserves nullable returns and facade-specific normalization", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { modelApi, mountApi } = await import("../ipc/commands");

    await expect(mountApi.capability()).resolves.toBeNull();
    await expect(modelApi.presets()).resolves.toEqual([]);
  });

  it("keeps live and durable terminal list results caller-selected", async () => {
    const live = [{ tabId: "live-1", sessionId: "session-1" }];
    const durable = [{ id: "durable-1", incarnation: "incarnation-1" }];
    runtime.Call.ByName
      .mockResolvedValueOnce({ ok: true, data: live })
      .mockResolvedValueOnce({ ok: true, data: durable });
    const { terminalApi } = await import("../ipc/commands");

    expectTypeOf(terminalApi.listLive).returns.toEqualTypeOf<Promise<import("../ipc/commands").LiveTabInfo[]>>();
    expectTypeOf(terminalApi.listDurable).returns.toEqualTypeOf<Promise<import("../ipc/commands").DurableDescriptor[]>>();
    await expect(terminalApi.listLive()).resolves.toBe(live);
    await expect(terminalApi.listDurable("session-1")).resolves.toBe(durable);
    expect(requestAt(0)).toEqual({ cmd: "terminal_list", args: null });
    expect(requestAt(1)).toEqual({
      cmd: "terminal_list",
      args: { sessionId: "session-1", durable: true },
    });
  });

  it("does not treat structured business errors as strings or transport exceptions", async () => {
    const error = {
      code: "host_key_pending",
      message: "Confirm host fingerprint",
      detail: { host: "example.test", port: 22, fingerprint: "SHA256:abc" },
    };
    runtime.Call.ByName.mockResolvedValue({ ok: false, error });
    const { sessionApi } = await import("../ipc/commands");

    await expect(sessionApi.connect("asset-1")).rejects.toEqual(error);
  });

  it("preserves file path normalization", async () => {
    runtime.Call.ByName.mockResolvedValue({
      ok: true,
      data: [{ path: "C:\\Users\\test\\file.txt" }],
    });
    const { fsApi } = await import("../ipc/commands");

    await expect(fsApi.list("session-1", "C:\\")).resolves.toEqual([
      { path: "C:/Users/test/file.txt" },
    ]);
  });

  it("passes transfer task ids, resume flags, and cancellation through the IPC envelope", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: 6 });
    const { fsApi } = await import("../ipc/commands");

    await fsApi.upload("session-1", "/local/source", "/remote/target", true, "task-upload", "attempt-upload");
    expect(requestAt(0)).toEqual({
      cmd: "fs_upload",
      args: {
        sessionId: "session-1",
        localPath: "/local/source",
        remotePath: "/remote/target",
        resume: true,
        taskId: "task-upload",
        attemptId: "attempt-upload",
      },
    });
    await fsApi.download("session-1", "/remote/source", "/local/target", true, "task-download", "attempt-download");
    expect(requestAt(1)).toEqual({
      cmd: "fs_download",
      args: {
        sessionId: "session-1",
        remotePath: "/remote/source",
        localPath: "/local/target",
        resume: true,
        taskId: "task-download",
        attemptId: "attempt-download",
      },
    });
    await fsApi.packDownload("session-1", "/remote/dir", "/local/archive", "task-pack", "attempt-pack");
    expect(requestAt(2)).toEqual({
      cmd: "fs_pack_download",
      args: {
        sessionId: "session-1",
        remotePath: "/remote/dir",
        localPath: "/local/archive",
        taskId: "task-pack",
        attemptId: "attempt-pack",
      },
    });
    await fsApi.write("session-1", "/remote/content", "YQ==", false, "task-write", "attempt-write");
    expect(requestAt(3)).toEqual({
      cmd: "fs_write",
      args: {
        args: {
          sessionId: "session-1",
          path: "/remote/content",
          contentBase64: "YQ==",
          backup: false,
          taskId: "task-write",
          attemptId: "attempt-write",
        },
      },
    });
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: true });
    await expect(fsApi.cancel("task-download", "attempt-download")).resolves.toBe(true);
    expect(requestAt(4)).toEqual({
      cmd: "fs_cancel",
      args: { taskId: "task-download", attemptId: "attempt-download" },
    });
  });

  it("passes chunked upload and native range arguments through flat envelopes", async () => {
    const { fsApi, localFileApi } = await import("../ipc/commands");
    const identity = { sessionId: "session-1", taskId: "task-1", attemptId: "attempt-1" };
    const upload = { ...identity, uploadId: "upload-1" };
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: { uploadId: "upload-1", chunkSize: 1048576 } });
    await fsApi.uploadBegin({ ...identity, remotePath: "/remote/file.bin", totalSize: 3, chunkSize: 1048576 });
    expect(requestAt(0)).toEqual({
      cmd: "fs_upload_begin",
      args: { ...identity, remotePath: "/remote/file.bin", totalSize: 3, chunkSize: 1048576 },
    });
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: { received: 3, total: 3 } });
    await fsApi.uploadChunk({ ...upload, index: 0, size: 3, sha256: "abc", contentBase64: "YQ==" });
    expect(requestAt(1)).toEqual({
      cmd: "fs_upload_chunk",
      args: { ...upload, index: 0, size: 3, sha256: "abc", contentBase64: "YQ==" },
    });
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: 3 });
    await fsApi.uploadCommit(upload);
    expect(requestAt(2)).toEqual({ cmd: "fs_upload_commit", args: upload });
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    await fsApi.uploadAbort(upload);
    expect(requestAt(3)).toEqual({ cmd: "fs_upload_abort", args: upload });
    runtime.Call.ByName.mockResolvedValue({
      ok: true,
      data: { path: "/local/file.bin", offset: 2, size: 3, contentBase64: "YQ==", truncated: false },
    });
    await localFileApi.readRange("/local/file.bin", 2, 1);
    expect(requestAt(4)).toEqual({
      cmd: "local_file_read_range",
      args: { path: "/local/file.bin", offset: 2, maxBytes: 1 },
    });
  });

  it("keeps desktop TypeErrors on the existing internal fallback", async () => {
    runtime.Call.ByName.mockRejectedValue(new TypeError("Failed to fetch"));
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).rejects.toEqual({
      code: "internal",
      message: "Failed to fetch",
    });
  });

  it("normalizes Windows roots without changing their capability metadata", async () => {
    runtime.Call.ByName.mockResolvedValue({
      ok: true,
      data: {
        pathStyle: "windows",
        roots: [
          { path: "C:\\", label: "C:" },
          { path: "D:\\", label: "D:" },
        ],
        homePath: "C:\\Users\\test",
        supported: true,
      },
    });
    const { fsApi } = await import("../ipc/commands");

    await expect(fsApi.roots("session-1")).resolves.toEqual({
      pathStyle: "windows",
      roots: [
        { path: "C:/", label: "C:" },
        { path: "D:/", label: "D:" },
      ],
      homePath: "C:/Users/test",
      supported: true,
    });
    expect(requestAt()).toEqual({ cmd: "fs_roots", args: { sessionId: "session-1" } });
  });

  it("keeps unsupported roots distinct from a supported empty result", async () => {
    runtime.Call.ByName.mockResolvedValueOnce({
      ok: true,
      data: { pathStyle: "unknown", roots: [], homePath: null, supported: false },
    });
    runtime.Call.ByName.mockResolvedValueOnce({
      ok: true,
      data: { pathStyle: "posix", roots: [], homePath: null, supported: true },
    });
    const { fsApi } = await import("../ipc/commands");

    await expect(fsApi.roots("session-unsupported")).resolves.toEqual({
      pathStyle: "unknown",
      roots: [],
      homePath: null,
      supported: false,
    });
    await expect(fsApi.roots("session-empty")).resolves.toEqual({
      pathStyle: "posix",
      roots: [],
      homePath: null,
      supported: true,
    });
    expect(requestAt(0)).toEqual({
      cmd: "fs_roots",
      args: { sessionId: "session-unsupported" },
    });
    expect(requestAt(1)).toEqual({ cmd: "fs_roots", args: { sessionId: "session-empty" } });
  });
});

describe("desktop transcript facade IPC contracts", () => {
  function serializedRequestAt(index: number): Record<string, unknown> {
    return JSON.parse(JSON.stringify(requestAt(index))) as Record<string, unknown>;
  }

  it("mirrors all six flat envelopes and preserves partial reads and mutation payloads", async () => {
    const partial = {
      chunks: [{ seq: 3, tabId: "tab-1", ts: 12, kind: 0, dataBase64: "AQID" }],
      nextSeq: 4,
      done: false,
      totalBytes: 9,
    };
    const results: Record<string, unknown> = {
      transcript_hosts: [{ assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 10 }],
      transcript_list: [{
        id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
        startedAt: 10, endedAt: 20, bytes: 3, storedBytes: 2, chunks: 1, truncated: false, active: false,
        syncOptIn: true, contentOmitted: false,
      }],
      transcript_read: partial,
      transcript_search: [{ seq: 3, ts: 12, preview: "match" }],
      transcript_sync_opt_in: {},
    };
    runtime.Call.ByName.mockImplementation(async (_name, request) => ({
      ok: true,
      data: results[(request as { cmd: string }).cmd] ?? null,
    }));
    const { transcriptApi } = await import("../ipc/commands");

    await transcriptApi.hosts();
    await transcriptApi.list("a1");
    await expect(transcriptApi.read("t1", 3, 1024)).resolves.toEqual(partial);
    await transcriptApi.search("t1", "match");
    await expect(transcriptApi.remove("t1")).resolves.toBeNull();
    await expect(transcriptApi.syncOptIn("t1", true)).resolves.toEqual({});
    await transcriptApi.read("t1");

    expect(serializedRequestAt(0)).toEqual({ cmd: "transcript_hosts", args: null });
    expect(serializedRequestAt(1)).toEqual({ cmd: "transcript_list", args: { assetId: "a1" } });
    expect(serializedRequestAt(2)).toEqual({ cmd: "transcript_read", args: { id: "t1", afterSeq: 3, maxBytes: 1024 } });
    expect(serializedRequestAt(3)).toEqual({ cmd: "transcript_search", args: { id: "t1", query: "match" } });
    expect(serializedRequestAt(4)).toEqual({ cmd: "transcript_delete", args: { id: "t1" } });
    expect(serializedRequestAt(5)).toEqual({ cmd: "transcript_sync_opt_in", args: { id: "t1", optIn: true } });
    expect(serializedRequestAt(6)).toEqual({ cmd: "transcript_read", args: { id: "t1", afterSeq: 0 } });
    expect(serializedRequestAt(5).args).not.toHaveProperty("args");
  });
});

describe("desktop database and Redis facade IPC contracts", () => {
  function serializedRequestAt(index: number): Record<string, unknown> {
    return JSON.parse(JSON.stringify(requestAt(index))) as Record<string, unknown>;
  }

  it("mirrors all 10 envelopes and preserves tuples, dynamic JSON and null results", async () => {
    const results: Record<string, unknown> = {
      db_connect: { connId: "conn-1" },
      db_schemas: ["public"],
      db_tables: ["orders"],
      db_columns: { columns: [], indexes: [] },
      db_query: { columns: ["id"], rows: [[{ dynamic: true }]], rowsAffected: 1, durationMs: 2, truncated: true, error: null },
      redis_scan: [4, ["key"]],
      redis_inspect: { key: "key", keyType: "string", ttl: -1, value: { dynamic: [1, null] } },
      redis_command: "PONG",
    };
    runtime.Call.ByName.mockImplementation(async (_name, request) => ({
      ok: true,
      data: results[(request as { cmd: string }).cmd] ?? null,
    }));
    const { dbApi } = await import("../ipc/commands");

    await expect(dbApi.connect("asset-1")).resolves.toEqual({ connId: "conn-1" });
    await expect(dbApi.disconnect("conn-1")).resolves.toBeNull();
    await dbApi.schemas("conn-1");
    await dbApi.tables("conn-1", "");
    await dbApi.columns("conn-1", "orders", "");
    await expect(dbApi.query("conn-1", "SELECT 1", 0)).resolves.toEqual(results.db_query);
    await expect(dbApi.redisScan("conn-1", 0, "", 0)).resolves.toEqual([4, ["key"]]);
    await expect(dbApi.redisInspect("conn-1", "")).resolves.toEqual(results.redis_inspect);
    await expect(dbApi.redisCommand("conn-1", ["PING", ""])).resolves.toBe("PONG");
    await expect(dbApi.redisSetTtl("conn-1", "", 0)).resolves.toBeNull();

    expect(serializedRequestAt(0)).toEqual({ cmd: "db_connect", args: { args: { assetId: "asset-1" } } });
    expect(serializedRequestAt(1)).toEqual({ cmd: "db_disconnect", args: { connId: "conn-1" } });
    expect(serializedRequestAt(2)).toEqual({ cmd: "db_schemas", args: { connId: "conn-1" } });
    expect(serializedRequestAt(3)).toEqual({ cmd: "db_tables", args: { connId: "conn-1", schema: "" } });
    expect(serializedRequestAt(4)).toEqual({ cmd: "db_columns", args: { connId: "conn-1", table: "orders", schema: "" } });
    expect(serializedRequestAt(5)).toEqual({ cmd: "db_query", args: { connId: "conn-1", sql: "SELECT 1", timeoutMs: 0 } });
    expect(serializedRequestAt(6)).toEqual({ cmd: "redis_scan", args: { connId: "conn-1", cursor: 0, pattern: "", count: 0 } });
    expect(serializedRequestAt(7)).toEqual({ cmd: "redis_inspect", args: { connId: "conn-1", key: "" } });
    expect(serializedRequestAt(8)).toEqual({ cmd: "redis_command", args: { connId: "conn-1", args: ["PING", ""] } });
    expect(serializedRequestAt(9)).toEqual({ cmd: "redis_set_ttl", args: { connId: "conn-1", key: "", seconds: 0 } });
  });

  it("mirrors web omission of unset facade options after desktop serialization", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { dbApi } = await import("../ipc/commands");

    await dbApi.tables("conn-1");
    await dbApi.columns("conn-1", "orders");
    await dbApi.query("conn-1", "SELECT 1");
    await dbApi.redisScan("conn-1");

    expect(serializedRequestAt(0)).toEqual({ cmd: "db_tables", args: { connId: "conn-1" } });
    expect(serializedRequestAt(1)).toEqual({ cmd: "db_columns", args: { connId: "conn-1", table: "orders" } });
    expect(serializedRequestAt(2)).toEqual({ cmd: "db_query", args: { connId: "conn-1", sql: "SELECT 1" } });
    expect(serializedRequestAt(3)).toEqual({ cmd: "redis_scan", args: { connId: "conn-1" } });
  });
});

describe("desktop platform, update and biometric facade IPC contracts", () => {
  it("preserves all 11 flat envelopes, null arguments and facade void results", async () => {
    const installResult = { installed: false, restarted: false, version: "", unavailableReason: "install unavailable" };
    const restartResult = { installed: true, restarted: false, version: "1.2.3", unavailableReason: "restart unavailable" };
    const results: Record<string, unknown> = {
      app_update_install: installResult,
      app_restart: restartResult,
      biometric_unlock: { ok: true, password: "stored-master-password" },
      desktop_tray_set: { enabled: false },
    };
    runtime.Call.ByName.mockImplementation(async (_name, request) => ({
      ok: true,
      data: results[(request as { cmd: string }).cmd] ?? null,
    }));
    const { appUpdateApi, biometricApi, desktopTrayApi, systemApi } = await import("../ipc/commands");
    const cases: Array<{ command: string; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "app_platform", payload: null, invoke: () => systemApi.platform() },
      { command: "system_insets", payload: null, invoke: () => systemApi.insets() },
      { command: "desktop_tray_get", payload: null, invoke: () => desktopTrayApi.get() },
      { command: "desktop_tray_set", payload: { enabled: false }, invoke: () => desktopTrayApi.set(false) },
      { command: "app_update_check", payload: null, invoke: () => appUpdateApi.check() },
      { command: "app_update_install", payload: { version: null }, invoke: async () => expect(await appUpdateApi.install()).toEqual(installResult) },
      { command: "app_restart", payload: null, invoke: async () => expect(await appUpdateApi.restart()).toEqual(restartResult) },
      { command: "biometric_status", payload: null, invoke: () => biometricApi.status() },
      { command: "biometric_unlock", payload: null, invoke: () => biometricApi.unlock() },
      { command: "biometric_enable", payload: { password: "master-password" }, invoke: async () => expect(await biometricApi.enable("master-password")).toBeNull() },
      { command: "biometric_disable", payload: null, invoke: async () => expect(await biometricApi.disable()).toBeNull() },
    ];

    expect(cases).toHaveLength(11);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expect(requestAt(index)).toEqual({ cmd: test.command, args: test.payload });
    }
    expect(runtime.Call.ByName).toHaveBeenCalledTimes(11);
  });

  it("preserves null, explicit and empty update versions on the desktop transport", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { appUpdateApi } = await import("../ipc/commands");

    await appUpdateApi.install();
    await appUpdateApi.install(undefined);
    await appUpdateApi.install("1.2.3");
    await appUpdateApi.install("");

    expect(requestAt(0)).toEqual({ cmd: "app_update_install", args: { version: null } });
    expect(requestAt(1)).toEqual({ cmd: "app_update_install", args: { version: null } });
    expect(requestAt(2)).toEqual({ cmd: "app_update_install", args: { version: "1.2.3" } });
    expect(requestAt(3)).toEqual({ cmd: "app_update_install", args: { version: "" } });
  });
});

describe("desktop forward runtime facade IPC contracts", () => {
  function serializedRequestAt(index: number): Record<string, unknown> {
    return JSON.parse(JSON.stringify(requestAt(index))) as Record<string, unknown>;
  }

  it("preserves all seven flat envelopes and the runtime null remove result", async () => {
    const environment = { available: true, platform: "other", listenHost: "127.0.0.1" };
    const spec = {
      id: "forward-1",
      sessionId: "session-1",
      hostId: "asset-1",
      status: "active",
      listenHost: "127.0.0.1",
      listenPort: 13306,
      targetHost: "db.internal",
      targetPort: 5432,
      kind: "local",
      createdAt: 1,
    };
    const results: Record<string, unknown> = {
      forward_env: environment,
      forward_create: spec,
      forward_create_socks: spec,
      forward_create_remote: spec,
      forward_list: [spec],
      forward_retry: spec,
    };
    runtime.Call.ByName.mockImplementation(async (_name, request) => ({
      ok: true,
      data: results[(request as { cmd: string }).cmd] ?? null,
    }));
    const { forwardApi } = await import("../ipc/commands");

    await expect(forwardApi.env()).resolves.toEqual(environment);
    await expect(forwardApi.create("session-1", 13306, "db.internal", 5432)).resolves.toEqual(spec);
    await expect(forwardApi.createSocks("session-1", 1080, true)).resolves.toEqual(spec);
    await expect(forwardApi.createRemote("session-1", "0.0.0.0", 18080, "127.0.0.1", 8080, true)).resolves.toEqual(spec);
    await expect(forwardApi.list()).resolves.toEqual([spec]);
    await expect(forwardApi.retry("forward-1", "session-1")).resolves.toEqual(spec);
    await expect(forwardApi.remove("forward-1")).resolves.toBeNull();

    expect(serializedRequestAt(0)).toEqual({ cmd: "forward_env", args: null });
    expect(serializedRequestAt(1)).toEqual({
      cmd: "forward_create",
      args: { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432 },
    });
    expect(serializedRequestAt(2)).toEqual({
      cmd: "forward_create_socks",
      args: { sessionId: "session-1", listenPort: 1080, acknowledgeRisk: true },
    });
    expect(serializedRequestAt(3)).toEqual({
      cmd: "forward_create_remote",
      args: { sessionId: "session-1", bindHost: "0.0.0.0", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080, acknowledgeRisk: true },
    });
    expect(serializedRequestAt(4)).toEqual({ cmd: "forward_list", args: null });
    expect(serializedRequestAt(5)).toEqual({ cmd: "forward_retry", args: { id: "forward-1", sessionId: "session-1" } });
    expect(serializedRequestAt(6)).toEqual({ cmd: "forward_remove", args: { id: "forward-1" } });
  });

  it("omits unacknowledged risk and unset or empty retry session identity", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { forwardApi } = await import("../ipc/commands");

    await forwardApi.create("session-1", 13306, "db.internal", 5432);
    await forwardApi.createSocks("session-1", 1080);
    await forwardApi.createSocks("session-1", 1080, false);
    await forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1", 8080);
    await forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1", 8080, false);
    await forwardApi.retry("forward-1");
    await forwardApi.retry("forward-1", undefined);
    await forwardApi.retry("forward-1", "");

    expect(serializedRequestAt(0)).toEqual({
      cmd: "forward_create",
      args: { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432 },
    });
    expect(serializedRequestAt(1)).toEqual({ cmd: "forward_create_socks", args: { sessionId: "session-1", listenPort: 1080 } });
    expect(serializedRequestAt(2)).toEqual({ cmd: "forward_create_socks", args: { sessionId: "session-1", listenPort: 1080 } });
    expect(serializedRequestAt(3)).toEqual({
      cmd: "forward_create_remote",
      args: { sessionId: "session-1", bindHost: "127.0.0.1", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080 },
    });
    expect(serializedRequestAt(4)).toEqual({
      cmd: "forward_create_remote",
      args: { sessionId: "session-1", bindHost: "127.0.0.1", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080 },
    });
    for (const index of [0, 1, 2, 3, 4]) {
      expect(serializedRequestAt(index).args).not.toHaveProperty("acknowledgeRisk");
    }
    for (const index of [5, 6, 7]) {
      expect(serializedRequestAt(index)).toEqual({ cmd: "forward_retry", args: { id: "forward-1" } });
    }
  });
});

describe("desktop operations facade IPC contracts", () => {
  function serializedRequestAt(index: number): Record<string, unknown> {
    return JSON.parse(JSON.stringify(requestAt(index))) as Record<string, unknown>;
  }

  it("mirrors all 12 envelopes and keeps layout conflicts and import warnings as results", async () => {
    const layoutData = { v: 3, workspaces: [{ id: "workspace-1", nested: [true, null] }] };
    const conflict = { saved: false, revision: 8, conflict: true };
    const applied = {
      assetsCreated: 1,
      assetsUpdated: 0,
      credentialsCreated: 1,
      credentialsUpdated: 0,
      skipped: 2,
      warnings: ["one entry was skipped"],
    };
    const results: Record<string, unknown> = {
      layout_get: { revision: 7, updatedAt: 1234, data: layoutData },
      layout_put: conflict,
      ssh_import_apply: applied,
    };
    runtime.Call.ByName.mockImplementation(async (_name, request) => ({
      ok: true,
      data: results[(request as { cmd: string }).cmd] ?? null,
    }));
    const { assetApi, layoutApi, sshImportApi } = await import("../ipc/commands");
    const layoutJSON = JSON.stringify(layoutData);

    await assetApi.snippetList();
    await assetApi.snippetCreate("Logs", "tail -f app.log", "group-1", 0);
    await assetApi.snippetUpdate("snippet-1", "Logs", "tail -f app.log", { groupId: null, sort: 0 });
    await assetApi.snippetDelete("snippet-1");
    await assetApi.auditQuery({ sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect", limit: 100, offset: 200 });
    await assetApi.auditCount({ sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect" });
    await assetApi.commandQuery({ sessionId: "session-1", assetId: "asset-1", userId: "user-1", limit: 100, offset: 200 });
    await assetApi.commandCount({ sessionId: "session-1", assetId: "asset-1", userId: "user-1" });
    await expect(layoutApi.get()).resolves.toEqual({ revision: 7, updatedAt: 1234, data: layoutData });
    await expect(layoutApi.put(layoutJSON, 7)).resolves.toEqual(conflict);
    await sshImportApi.preview({ source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true });
    await expect(sshImportApi.apply({
      source: "ssh-config",
      path: "/home/demo/.ssh/config",
      confirmed: true,
      groupId: "group-1",
      tags: "prod, 欧洲",
      hosts: [{ id: "host-1", action: "import" }],
      keys: [{ id: "key-1", action: "skip" }],
    })).resolves.toEqual(applied);

    const expected: Array<{ cmd: string; args: unknown }> = [
      { cmd: "snippet_list", args: null },
      { cmd: "snippet_create", args: { name: "Logs", body: "tail -f app.log", groupId: "group-1", sort: 0 } },
      { cmd: "snippet_update", args: { id: "snippet-1", name: "Logs", body: "tail -f app.log", groupId: null, sort: 0 } },
      { cmd: "snippet_delete", args: { id: "snippet-1" } },
      { cmd: "audit_query", args: { args: { sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect", limit: 100, offset: 200 } } },
      { cmd: "audit_count", args: { args: { sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect" } } },
      { cmd: "command_query", args: { args: { sessionId: "session-1", assetId: "asset-1", userId: "user-1", limit: 100, offset: 200 } } },
      { cmd: "command_count", args: { args: { sessionId: "session-1", assetId: "asset-1", userId: "user-1" } } },
      { cmd: "layout_get", args: null },
      { cmd: "layout_put", args: { args: { data: layoutJSON, revision: 7 } } },
      { cmd: "ssh_import_preview", args: { args: { source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true } } },
      { cmd: "ssh_import_apply", args: { args: { source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true, groupId: "group-1", tags: "prod, 欧洲", hosts: [{ id: "host-1", action: "import" }], keys: [{ id: "key-1", action: "skip" }] } } },
    ];
    expect(expected).toHaveLength(12);
    expected.forEach((want, index) => expect(serializedRequestAt(index)).toEqual(want));
    expect(runtime.Call.ByName).toHaveBeenCalledTimes(12);
  });
});

describe("web command behavior compatibility", () => {
  function useWebTransport() {
    vi.stubGlobal("window", {
      __NEXTERM_TRANSPORT__: "web",
      location: { search: "" },
    });
  }

  it("still sends the original /rpc envelope", async () => {
    useWebTransport();
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      text: async () => JSON.stringify({ ok: true, data: "linux" }),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { systemApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(systemApi.platform()).resolves.toBe("linux");
    const [url, init] = fetchMock.mock.calls[0] as [string, { body: string }];
    expect(url).toBe("/rpc");
    expect(JSON.parse(init.body)).toEqual({ cmd: "app_platform", args: null });
    expect(runtime.Call.ByName).not.toHaveBeenCalled();
  });

  it("places channel at the top level for web takeover", async () => {
    useWebTransport();
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      text: async () => JSON.stringify({ ok: true, data: { jobId: "job-1", token: "token-1" } }),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { aiApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await aiApi.takeoverRun({
      tabId: "tab-1",
      token: "token-1",
      instruction: "Check load",
      allowWrite: false,
      channel: { toJSON: () => "ai-1" },
    });
    const [, init] = fetchMock.mock.calls[0] as [string, { body: string }];
    expect(JSON.parse(init.body)).toEqual({
      cmd: "ai_takeover_run",
      args: { tabId: "tab-1", token: "token-1", instruction: "Check load", allowWrite: false },
      channel: "ai-1",
    });
  });

  it("uses the same stable client identity for web line tab open and exec", async () => {
    useWebTransport();
    Object.assign(window, {
      localStorage: {
        getItem: vi.fn().mockReturnValue("web-client"),
        setItem: vi.fn(),
      },
    });
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      text: async () => JSON.stringify({ ok: true, data: null }),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { sessionApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await sessionApi.openLineTab("session-1", 120, 40, { toJSON: () => "line-1" });
    await sessionApi.lineExec("tab-1", "Set-Location -LiteralPath 'D:/Project'");
    const openRequest = JSON.parse(fetchMock.mock.calls[0][1].body);
    const execRequest = JSON.parse(fetchMock.mock.calls[1][1].body);

    expect(openRequest).toEqual({
      cmd: "session_open_line_tab",
      args: { sessionId: "session-1", cols: 120, rows: 40 },
      channel: "line-1",
      clientId: "web-client",
    });
    expect(execRequest).toEqual({
      cmd: "session_line_exec",
      args: { tabId: "tab-1", line: "Set-Location -LiteralPath 'D:/Project'" },
      clientId: "web-client",
    });
  });

  it("preserves code/detail for business errors returned with HTTP 200", async () => {
    useWebTransport();
    const error = { code: "forbidden", message: "Not allowed", detail: null };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        status: 200,
        text: async () => JSON.stringify({ ok: false, error }),
      }),
    );
    const { terminalApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(terminalApi.write("tab-1", new Uint8Array([1]))).rejects.toEqual(error);
  });

  it("classifies a browser fetch TypeError as disconnected without retrying the query", async () => {
    useWebTransport();
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetchMock);
    const { dbApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(dbApi.query("c1", "UPDATE orders SET paid = true")).rejects.toEqual({
      code: "disconnected",
      message: "Failed to fetch",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("classifies a response body TypeError as disconnected", async () => {
    useWebTransport();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        status: 200,
        text: async () => Promise.reject(new TypeError("Response body terminated")),
      }),
    );
    const { dbApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(dbApi.query("c1", "INSERT INTO orders VALUES (1)")).rejects.toEqual({
      code: "disconnected",
      message: "Response body terminated",
    });
  });

  it("does not infer disconnection from network-like text on an ordinary Error", async () => {
    useWebTransport();
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("Failed to fetch")));
    const { dbApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(dbApi.query("c1", "SELECT 1")).rejects.toEqual({
      code: "internal",
      message: "Failed to fetch",
    });
  });

  it("preserves HTTP error envelopes and non-JSON protocol errors", async () => {
    useWebTransport();
    const error = { code: "unavailable", message: "Service unavailable" };
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce({
          status: 503,
          text: async () => JSON.stringify({ ok: false, error }),
        })
        .mockResolvedValueOnce({ status: 502, text: async () => "<html>Bad Gateway</html>" }),
    );
    const { systemApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(systemApi.platform()).rejects.toEqual(error);
    await expect(systemApi.platform()).rejects.toMatchObject({
      code: "internal",
      message: expect.stringContaining("HTTP 502"),
    });
  });
});
