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

describe("desktop 命令 envelope", () => {
  it("无参数命令发送 args:null", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: "macos" });
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).resolves.toBe("macos");
    expect(runtime.Call.ByName).toHaveBeenCalledWith("main.Service.Call", {
      cmd: "app_platform",
      args: null,
    });
  });

  it("提升 channel/clientId，同时保留扁平参数", async () => {
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

  it("nested args 内的 clientId 不被提升或拍平", async () => {
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

  it("takeover 保持扁平正文和顶层 channel", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: "job-1" });
    const { aiApi } = await import("../ipc/commands");

    await aiApi.takeoverRun({
      tabId: "tab-1",
      instruction: "检查负载",
      allowWrite: false,
      channel: { toJSON: () => "ai-1" },
    });
    expect(JSON.parse(JSON.stringify(requestAt()))).toEqual({
      cmd: "ai_takeover_run",
      args: { tabId: "tab-1", instruction: "检查负载", allowWrite: false },
      channel: "ai-1",
    });
  });

  it("保留 nullable 返回和 facade 自身归一化", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { terminalApi, modelApi, mountApi } = await import("../ipc/commands");

    await expect(terminalApi.claim("tab-1")).resolves.toBeNull();
    await expect(mountApi.capability()).resolves.toBeNull();
    await expect(modelApi.presets()).resolves.toEqual([]);
  });

  it("结构化业务错误不按字符串或传输异常处理", async () => {
    const error = {
      code: "host_key_pending",
      message: "确认主机指纹",
      detail: { host: "example.test", port: 22, fingerprint: "SHA256:abc" },
    };
    runtime.Call.ByName.mockResolvedValue({ ok: false, error });
    const { sessionApi } = await import("../ipc/commands");

    await expect(sessionApi.connect("asset-1")).rejects.toEqual(error);
  });

  it("保留文件路径归一化", async () => {
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

describe("web 命令行为保持", () => {
  function useWebTransport() {
    vi.stubGlobal("window", {
      __NEXTERM_TRANSPORT__: "web",
      location: { search: "" },
    });
  }

  it("仍发送原 /rpc envelope", async () => {
    useWebTransport();
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      text: async () => JSON.stringify({ ok: true, data: "linux" }),
    });
    vi.stubGlobal("fetch", fetchMock);
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).resolves.toBe("linux");
    const [url, init] = fetchMock.mock.calls[0] as [string, { body: string }];
    expect(url).toBe("/rpc");
    expect(JSON.parse(init.body)).toEqual({ cmd: "app_platform", args: null });
    expect(runtime.Call.ByName).not.toHaveBeenCalled();
  });

  it("HTTP 200 业务错误仍保留 code/detail", async () => {
    useWebTransport();
    const error = { code: "not_controller", message: "仅观察", detail: null };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        status: 200,
        text: async () => JSON.stringify({ ok: false, error }),
      }),
    );
    const { terminalApi } = await import("../ipc/commands");

    await expect(terminalApi.write("tab-1", new Uint8Array([1]))).rejects.toEqual(error);
  });
});
