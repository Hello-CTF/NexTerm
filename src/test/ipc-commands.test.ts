import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
    const { terminalApi, modelApi, mountApi } = await import("../ipc/commands");

    await expect(terminalApi.claim("tab-1")).resolves.toBeNull();
    await expect(mountApi.capability()).resolves.toBeNull();
    await expect(modelApi.presets()).resolves.toEqual([]);
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
    const error = { code: "not_controller", message: "Observer only", detail: null };
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
