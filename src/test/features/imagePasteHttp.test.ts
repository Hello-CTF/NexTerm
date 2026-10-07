/** @vitest-environment jsdom */
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";

// 非 mock HTTP 契约: 用真实 node http 服务复刻 M130 的会话/CSRF 语义
// (无会话 401, 有会话缺/错 X-NexTerm-CSRF 403, 齐全 200), 验证 uploadImage 使用
// M125 落地后的账号会话(getCsrfToken 携带 CSRF 头, 401 派发 SESSION_EXPIRED_EVENT
// 交给账号门)。cookie 由最小 cookie jar 模拟浏览器行为。
vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
});

import { uploadImage, type ImageUploadError } from "../../ipc/webFiles";
import { describeImageUploadFailure } from "../../features/terminal/imagePaste";
import {
  authApi,
  getCsrfToken,
  setCsrfToken,
  SESSION_EXPIRED_EVENT,
} from "../../ipc/authApi";
import { setServerToken, clearServerToken } from "../../ipc/serverAuth";

interface ObservedRequest {
  path: string;
  cookie: string | null;
  csrf: string | null;
}

const observed: ObservedRequest[] = [];
let server: Server;
let baseUrl = "";
let jarCookie = "";

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let raw = "";
    const decoder = new TextDecoder();
    req.on("data", (chunk: Uint8Array) => (raw += decoder.decode(chunk, { stream: true })));
    req.on("end", () => resolve(raw));
  });
}

function sendJSON(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "content-type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body));
}

function firstHeader(value: string | string[] | undefined): string | null {
  if (value === undefined) return null;
  return Array.isArray(value) ? (value[0] ?? null) : value;
}

beforeAll(async () => {
  server = createServer((req: IncomingMessage, res: ServerResponse) => {
    void (async () => {
      const cookie = firstHeader(req.headers.cookie);
      const csrf = firstHeader(req.headers["x-nexterm-csrf"]);
      observed.push({ path: req.url ?? "", cookie, csrf });
      if (req.method === "POST" && req.url === "/auth/login") {
        const body = JSON.parse(await readBody(req)) as { username?: string; password?: string };
        if (body.username === "u" && body.password === "p") {
          res.setHeader(
            "set-cookie",
            "nexterm_session=s-1; Path=/; HttpOnly; SameSite=Lax",
          );
          sendJSON(res, 200, { user: { id: "u1", username: "u" }, csrf_token: "csrf-valid" });
        } else {
          sendJSON(res, 401, { ok: false, error: { code: "forbidden", message: "用户名或密码错误" } });
        }
        return;
      }
      if (req.method === "POST" && req.url?.startsWith("/files/image")) {
        if (!cookie) {
          sendJSON(res, 401, { ok: false, error: { code: "forbidden", message: "会话无效或缺失" } });
          return;
        }
        if (csrf !== "csrf-valid") {
          sendJSON(res, 403, { ok: false, error: { code: "forbidden", message: "CSRF 校验失败" } });
          return;
        }
        sendJSON(res, 200, {
          id: "01J",
          url: "/files/image/01J",
          mime: "image/png",
          bytes: 4,
          created_at: 1,
          expires_at: 2,
        });
        return;
      }
      sendJSON(res, 404, { ok: false, error: { code: "not_found", message: "not found" } });
    })();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("server address unavailable");
  baseUrl = `http://127.0.0.1:${address.port}`;
});

afterAll(async () => {
  await new Promise<void>((resolve, reject) =>
    server.close((err?: Error) => (err ? reject(err) : resolve())),
  );
});

// vitest 配置 unstubGlobals: true 会在每个测试前自动 unstub, 所以 cookie jar 包装放在 beforeEach。
beforeEach(() => {
  observed.length = 0;
  jarCookie = "";
  clearServerToken();
  setCsrfToken(null);
  window.localStorage.setItem("nexterm.api", baseUrl);

  const realFetch = globalThis.fetch;
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers as HeadersInit | undefined);
    if (jarCookie) headers.set("cookie", jarCookie);
    return realFetch(input, { ...init, headers }).then((res) => {
      const setCookie = res.headers.get("set-cookie");
      if (setCookie) jarCookie = setCookie.split(";")[0] ?? "";
      return res;
    });
  });
});

afterEach(() => {
  window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
  onExpired = () => {};
});

let onExpired: () => void = () => {};

function watchExpired(): { fired: () => boolean } {
  let fired = false;
  onExpired = () => {
    fired = true;
  };
  window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
  return { fired: () => fired };
}

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

describe("uploadImage 会话/CSRF HTTP 契约", () => {
  it("已登录会话携带 X-NexTerm-CSRF 成功上传", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-valid");

    const result = await uploadImage(png("a.png"));

    expect(result.url).toBe("/files/image/01J");
    expect(observed).toHaveLength(1);
    expect(observed[0]?.cookie).toBe("nexterm_session=s-1");
    expect(observed[0]?.csrf).toBe("csrf-valid");
  });

  it("authApi.login 建立会话后可直接上传(复用 M125 登录 API)", async () => {
    const session = await authApi.login("u", "p");

    expect(session.user.username).toBe("u");
    expect(getCsrfToken()).toBe("csrf-valid");

    const result = await uploadImage(png("b.png"));

    expect(result.url).toBe("/files/image/01J");
    const upload = observed.filter((entry) => entry.path.startsWith("/files/image"));
    expect(upload).toHaveLength(1);
    expect(upload[0]?.cookie).toBe("nexterm_session=s-1");
    expect(upload[0]?.csrf).toBe("csrf-valid");
  });

  it("有会话但缺 CSRF 头时 403 并映射后端文本", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken(null);

    const failure = await uploadImage(png("c.png")).catch((e: unknown) => e);

    expect((failure as ImageUploadError).status).toBe(403);
    expect((failure as ImageUploadError).bodyText).toContain("CSRF 校验失败");
    expect(describeImageUploadFailure(failure)).toContain("CSRF 校验失败");
    expect(observed).toHaveLength(1);
  });

  it("错误 CSRF 同样 403", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-wrong");

    const failure = await uploadImage(png("d.png")).catch((e: unknown) => e);

    expect((failure as ImageUploadError).status).toBe(403);
    expect((failure as ImageUploadError).bodyText).toContain("CSRF 校验失败");
  });

  it("无会话 401: 派发 SESSION_EXPIRED_EVENT 交给账号门, 不自行重试", async () => {
    const expired = watchExpired();

    const failure = await uploadImage(png("e.png")).catch((e: unknown) => e);

    expect((failure as ImageUploadError).status).toBe(401);
    expect((failure as ImageUploadError).bodyText).toContain("会话无效或缺失");
    expect(describeImageUploadFailure(failure)).toContain("需要登录或没有权限");
    expect(expired.fired()).toBe(true);
    expect(observed.filter((entry) => entry.path.startsWith("/files/image"))).toHaveLength(1);
  });

  it("静态同步令牌不能替代账号会话", async () => {
    setServerToken("static-token");
    const expired = watchExpired();

    const failure = await uploadImage(png("f.png")).catch((e: unknown) => e);

    expect((failure as ImageUploadError).status).toBe(401);
    expect(describeImageUploadFailure(failure)).toContain("需要登录或没有权限");
    expect(expired.fired()).toBe(true);
    expect(observed.filter((entry) => entry.path.startsWith("/files/image"))).toHaveLength(1);
  });
});
