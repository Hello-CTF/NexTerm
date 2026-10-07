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

  it("无会话(loopback/未登录)不携带 CSRF 头且请求照常", async () => {
    useWebTransport();
    const calls = captureFetch(() => ({ status: 200, body: JSON.stringify({ ok: true, data: [] }) }));
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    await expect(commands.assetApi.list()).resolves.toEqual([]);

    expect(calls).toHaveLength(1);
    expect(calls[0].headers["X-NexTerm-CSRF"]).toBeUndefined();
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
