import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const RPC_CSRF_FAILURE = JSON.stringify({
  ok: false,
  error: { code: "forbidden", message: "CSRF 校验失败" },
});

interface CapturedCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
}

function jsonResponse(status: number, body: string) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => body,
  };
}

function useWebTransport() {
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "" },
  });
}

function captureFetch(handler: (url: string) => { status: number; body: string }) {
  const calls: CapturedCall[] = [];
  const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
    const target = String(url);
    calls.push({
      url: target,
      method: init.method ?? "GET",
      headers: (init.headers ?? {}) as Record<string, string>,
      ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    const { status, body } = handler(target);
    return jsonResponse(status, body);
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

async function importWeb() {
  const commands = await import("../ipc/commands");
  const authApi = await import("../ipc/authApi");
  return { commands, authApi };
}

beforeEach(() => {
  vi.resetModules();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("web /rpc CSRF 传输", () => {
  it("带账号会话时 /rpc 请求携带 X-NexTerm-CSRF 头", async () => {
    useWebTransport();
    const calls = captureFetch(() => ({ status: 200, body: JSON.stringify({ ok: true, data: [] }) }));
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.list()).resolves.toEqual([]);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/rpc");
    expect(calls[0].method).toBe("POST");
    expect(calls[0].headers["X-NexTerm-CSRF"]).toBe("csrf-1");
  });

  it("无会话(loopback/auth=off): 共享预取 /auth/me 失败后 /rpc 不携带 CSRF 头照常发出", async () => {
    useWebTransport();
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        // auth=off 由 accountGuard 统一 403 拒绝账号路由
        return {
          status: 403,
          body: JSON.stringify({
            ok: false,
            error: { code: "forbidden", message: "当前为共享工作区模式（未启用账号登录），账号功能不可用" },
          }),
        };
      }
      return { status: 200, body: JSON.stringify({ ok: true, data: [] }) };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    await expect(commands.assetApi.list()).resolves.toEqual([]);
    await expect(commands.sessionApi.list()).resolves.toEqual([]);

    const rpcCalls = calls.filter((call) => call.url === "/rpc");
    expect(rpcCalls).toHaveLength(2);
    expect(rpcCalls[0].headers["X-NexTerm-CSRF"]).toBeUndefined();
    // 预取只发生一次, 失败结果被记住, 后续 /rpc 直接发出不再重复预取
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
  });

  it("冷启动 token 缺失: 并发 /rpc 共享一次 /auth/me, 全部等待后携带新令牌发出, 零 403", async () => {
    useWebTransport();
    let resolveMe: (value: { status: number; body: string }) => void = () => {};
    const meGate = new Promise<{ status: number; body: string }>((resolve) => {
      resolveMe = resolve;
    });
    const calls: CapturedCall[] = [];
    const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
      const target = String(url);
      calls.push({
        url: target,
        method: init.method ?? "GET",
        headers: (init.headers ?? {}) as Record<string, string>,
        ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
      });
      const { status, body } = target.includes("/auth/me")
        ? await meGate
        : { status: 200, body: JSON.stringify({ ok: true, data: null }) };
      return jsonResponse(status, body);
    });
    vi.stubGlobal("fetch", fetchMock);
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    const pending = Promise.all([
      commands.assetApi.list(),
      commands.sessionApi.list(),
      commands.vaultApi.status(),
    ]);
    // me 未返回前: /auth/me 只发出一次, 三个 /rpc 全部在等待, 一个都还没发出
    await vi.waitFor(() => {
      expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
    });
    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(0);

    resolveMe({
      status: 200,
      body: JSON.stringify({ user: { id: "u-1", username: "alice" }, csrf_token: "csrf-new" }),
    });
    await pending;

    const rpcCalls = calls.filter((call) => call.url === "/rpc");
    expect(rpcCalls).toHaveLength(3);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
    for (const call of rpcCalls) {
      expect(call.headers["X-NexTerm-CSRF"]).toBe("csrf-new");
    }
    expect(authApi.getCsrfToken()).toBe("csrf-new");
  });

  it("未登录: 共享预取 401 照常派发 SESSION_EXPIRED, 请求按现状走 403 重试路径抛错, 后续调用不挂起", async () => {
    const dispatchEvent = vi.fn();
    vi.stubGlobal("window", {
      __NEXTERM_TRANSPORT__: "web",
      location: { search: "" },
      dispatchEvent,
    });
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        return {
          status: 401,
          body: JSON.stringify({ ok: false, error: { code: "forbidden", message: "会话无效或缺失" } }),
        };
      }
      return { status: 403, body: RPC_CSRF_FAILURE };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    await expect(commands.assetApi.list()).rejects.toMatchObject({
      code: "forbidden",
      message: "会话无效或缺失",
    });
    expect(dispatchEvent).toHaveBeenCalled();

    // 后续调用不被挂起也不重复预取: 直接裸发 /rpc, 经 403 重试路径后同样抛错
    await expect(commands.sessionApi.list()).rejects.toMatchObject({
      code: "forbidden",
      message: "会话无效或缺失",
    });
    // /auth/me 共 3 次: 1 次共享预取 + 两次请求各 1 次 403 重试刷新
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(3);
    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(2);
  });

  it("CSRF 403 时经 /auth/me 刷新并重试同一请求一次,重试携带新令牌", async () => {
    useWebTransport();
    let rpcAttempts = 0;
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        return {
          status: 200,
          body: JSON.stringify({ user: { id: "u-1", username: "alice" }, csrf_token: "csrf-2" }),
        };
      }
      rpcAttempts += 1;
      if (rpcAttempts === 1) return { status: 403, body: RPC_CSRF_FAILURE };
      return { status: 200, body: JSON.stringify({ ok: true, data: [{ id: "a-1" }] }) };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.list()).resolves.toEqual([{ id: "a-1" }]);

    const rpcCalls = calls.filter((call) => call.url === "/rpc");
    expect(rpcCalls).toHaveLength(2);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
    expect(rpcCalls[0].headers["X-NexTerm-CSRF"]).toBe("csrf-1");
    expect(rpcCalls[1].headers["X-NexTerm-CSRF"]).toBe("csrf-2");
    expect(rpcCalls[1].method).toBe("POST");
    expect(rpcCalls[1].body).toEqual(rpcCalls[0].body);
    expect(authApi.getCsrfToken()).toBe("csrf-2");
  });

  it("重试后仍是 CSRF 403 时不再重试", async () => {
    useWebTransport();
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        return {
          status: 200,
          body: JSON.stringify({ user: { id: "u-1", username: "alice" }, csrf_token: "csrf-2" }),
        };
      }
      return { status: 403, body: RPC_CSRF_FAILURE };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.list()).rejects.toEqual({
      code: "forbidden",
      message: "CSRF 校验失败",
    });

    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(2);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
  });

  it("非 CSRF 的 403 不重试也不刷新会话", async () => {
    useWebTransport();
    const error = { code: "forbidden", message: "没有权限" };
    const calls = captureFetch(() => ({ status: 403, body: JSON.stringify({ ok: false, error }) }));
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.groupCreate("g")).rejects.toEqual(error);

    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(1);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(0);
  });
});
