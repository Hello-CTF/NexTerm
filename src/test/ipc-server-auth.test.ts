import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

function webWindow() {
  return {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "", protocol: "http:", host: "127.0.0.1:9" },
    setTimeout: (...args: Parameters<typeof setTimeout>) => globalThis.setTimeout(...args),
    addEventListener: () => {},
    dispatchEvent: () => true,
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    status,
    text: async () => JSON.stringify(body),
  } as Response;
}

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", webWindow());
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("authedFetch 401 处理", () => {
  it("无 401 时原样返回", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);
    const auth = await import("../ipc/serverAuth");

    const response = await auth.authedFetch("/rpc", { method: "POST" });
    expect(response.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("401 时派发重新登录事件并返回 401", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(401, { ok: false, error: { code: "forbidden", message: "会话无效或缺失" } }));
    vi.stubGlobal("fetch", fetchMock);
    const auth = await import("../ipc/serverAuth");

    const events: string[] = [];
    vi.stubGlobal("window", {
      ...webWindow(),
      dispatchEvent: (event: Event) => {
        events.push(event.type);
        return true;
      },
    });

    const response = await auth.authedFetch("/rpc", { method: "POST" });
    expect(response.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(events).toContain("nexterm:session-expired");
  });

  it("401 后不自动重试", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { ok: false }))
      .mockResolvedValueOnce(jsonResponse(200, { ok: true, data: "linux" }));
    vi.stubGlobal("fetch", fetchMock);
    const auth = await import("../ipc/serverAuth");

    const response = await auth.authedFetch("/rpc", { method: "POST" });
    expect(response.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("callWeb 经 401 直接失败", () => {
  it("首个调用 401 时直接失败,不重放", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { ok: false, error: { code: "forbidden", message: "会话无效或缺失" } }))
      .mockResolvedValueOnce(jsonResponse(200, { ok: true, data: "linux" }));
    vi.stubGlobal("fetch", fetchMock);
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).rejects.toMatchObject({ code: "forbidden" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("WebSocket 固定子协议", () => {
  class RecordingWebSocket {
    static instances: RecordingWebSocket[] = [];
    readonly url: string;
    readonly protocols: string[] | undefined;
    onopen: ((event: unknown) => void) | null = null;
    onmessage: ((event: { data: unknown }) => void) | null = null;
    onclose: ((event: unknown) => void) | null = null;
    onerror: (() => void) | null = null;

    constructor(url: string, protocols?: string[]) {
      this.url = url;
      this.protocols = protocols;
      RecordingWebSocket.instances.push(this);
    }

    addEventListener() {}
    close() {}
  }

  beforeEach(() => {
    RecordingWebSocket.instances = [];
    vi.stubGlobal("WebSocket", RecordingWebSocket);
  });

  it("channel 与 events 连接固定只提供 nexterm 子协议", async () => {
    const { newBinaryChannel, subscribeEvent } = await import("../ipc/webTransport");

    newBinaryChannel();
    expect(RecordingWebSocket.instances[0].protocols).toEqual(["nexterm"]);

    subscribeEvent("session://status", () => {});
    expect(RecordingWebSocket.instances[1].url).toContain("/ws/events");
    expect(RecordingWebSocket.instances[1].protocols).toEqual(["nexterm"]);
  });
});
