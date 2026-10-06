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

describe("serverAuth 令牌持有", () => {
  it("令牌只保存在内存：不读写任何前端存储", async () => {
    const setItem = vi.fn();
    const sessionSetItem = vi.fn();
    vi.stubGlobal("window", {
      ...webWindow(),
      localStorage: { getItem: () => null, setItem },
      sessionStorage: { getItem: () => null, setItem: sessionSetItem },
    });
    const auth = await import("../ipc/serverAuth");

    auth.setServerToken("  secret-token  ");
    expect(auth.getServerToken()).toBe("secret-token");
    expect(auth.authHeaders()).toEqual({ "X-NexTerm-Sync-Token": "secret-token" });
    expect(auth.wsAuthProtocols()).toEqual(["nexterm", "secret-token"]);

    auth.clearServerToken();
    expect(auth.getServerToken()).toBeNull();
    expect(auth.authHeaders()).toEqual({});
    expect(auth.wsAuthProtocols()).toEqual(["nexterm"]);

    for (const call of setItem.mock.calls) {
      expect(String(call[1])).not.toContain("secret-token");
    }
    expect(sessionSetItem).not.toHaveBeenCalled();
  });

  it("空白令牌归一化为未设置", async () => {
    const auth = await import("../ipc/serverAuth");
    auth.setServerToken("   ");
    expect(auth.getServerToken()).toBeNull();
  });
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

  it("401 时清空令牌、派发重新登录事件并返回 401(不再弹令牌框重试)", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(401, { ok: false, error: { code: "forbidden", message: "访问令牌无效或缺失" } }));
    vi.stubGlobal("fetch", fetchMock);
    const auth = await import("../ipc/serverAuth");
    auth.setServerToken("stale-token");

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
    expect(auth.getServerToken()).toBeNull();
    expect(events).toContain("nexterm:session-expired");
  });

  it("401 后不再以新令牌重试", async () => {
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

describe("callWeb 经 401 不再索取令牌", () => {
  it("首个调用 401 时直接失败,不重放", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { ok: false, error: { code: "forbidden", message: "访问令牌无效或缺失" } }))
      .mockResolvedValueOnce(jsonResponse(200, { ok: true, data: "linux" }));
    vi.stubGlobal("fetch", fetchMock);
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).rejects.toMatchObject({ code: "forbidden" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("WebSocket 子协议携带令牌", () => {
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

  it("无令牌只提供 nexterm 子协议，有令牌附带令牌", async () => {
    const auth = await import("../ipc/serverAuth");
    const { newBinaryChannel } = await import("../ipc/webTransport");

    newBinaryChannel();
    expect(RecordingWebSocket.instances[0].protocols).toEqual(["nexterm"]);

    auth.setServerToken("ws-token");
    newBinaryChannel();
    expect(RecordingWebSocket.instances[1].protocols).toEqual(["nexterm", "ws-token"]);
  });

  it("events 连接同样携带当前令牌", async () => {
    const auth = await import("../ipc/serverAuth");
    auth.setServerToken("ws-token");
    const { subscribeEvent } = await import("../ipc/webTransport");

    subscribeEvent("session://status", () => {});
    expect(RecordingWebSocket.instances[0].url).toContain("/ws/events");
    expect(RecordingWebSocket.instances[0].protocols).toEqual(["nexterm", "ws-token"]);
  });
});
