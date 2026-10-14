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
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: "tab-1" });
    const { terminalApi } = await import("../ipc/commands");
    const channel = { toJSON: () => "pty-1" };

    await terminalApi.attach("session-1", 120, 40, channel);
    const request = requestAt();
    expect(JSON.parse(JSON.stringify(request))).toEqual({
      cmd: "terminal_attach",
      args: { sessionId: "session-1", cols: 120, rows: 40 },
      channel: "pty-1",
      clientId: expect.any(String),
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
});
