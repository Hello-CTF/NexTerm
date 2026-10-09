/** @vitest-environment jsdom */
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";

// 非 mock HTTP 契约: 用真实 node http 服务复刻 requireAuth 的会话/CSRF 语义
// (cookie 会话的 POST/DELETE 缺/错 X-NexTerm-CSRF 时 403 + "CSRF 校验失败",
// 且拒绝发生在中间件、handler 不执行), 验证 stageFile/reserveTarget/deleteBlob
// 携带 M125 账号会话 CSRF 头, 精确 CSRF 403 时经 /auth/me 刷新并重试同一请求一次,
// 其他 403 不重试, 上传/删除不因重试重复执行。
vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
});

import { deliverStaged, dropStaged, requestSaveTarget, stageFile } from "../ipc/webFiles";
import { authApi, getCsrfToken, isMfaChallenge, setCsrfToken } from "../ipc/authApi";
import { clearServerToken } from "../ipc/serverAuth";

interface ObservedRequest {
  method: string;
  path: string;
  cookie: string | null;
  csrf: string | null;
  body: string;
}

interface BlobRecord {
  id: string;
  path: string;
  body: string;
}

const observed: ObservedRequest[] = [];
// handler 执行次数与中间件拒绝分开计数: CSRF 拒绝不得触达 handler。
const handled = { stage: 0, reserve: 0, remove: 0 };
const stagedBlobs = new Map<string, BlobRecord>();
let server: Server;
let baseUrl = "";
let jarCookie = "";
let currentCsrf = "csrf-1";
let alwaysCsrfFail = false;
let blobPostForbidden = false;
let blobSeq = 0;

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

// 复刻 requireAuth: 无 cookie 视为 loopback/auth=off 放行; 有 cookie 的
// POST/DELETE 必须携带与会话一致的 CSRF 头, 否则 403 且不触达 handler。
function sessionRejected(req: IncomingMessage, cookie: string | null, csrf: string | null): boolean {
  if (!cookie) return false;
  if (req.method !== "POST" && req.method !== "DELETE") return false;
  return alwaysCsrfFail || csrf !== currentCsrf;
}

beforeAll(async () => {
  server = createServer((req: IncomingMessage, res: ServerResponse) => {
    void (async () => {
      const cookie = firstHeader(req.headers.cookie);
      const csrf = firstHeader(req.headers["x-nexterm-csrf"]);
      const path = req.url ?? "";
      const body = await readBody(req);
      observed.push({ method: req.method ?? "GET", path, cookie, csrf, body });
      if (req.method === "POST" && path === "/auth/login") {
        res.setHeader("set-cookie", "nexterm_session=s-1; Path=/; HttpOnly; SameSite=Lax");
        sendJSON(res, 200, { user: { id: "u1", username: "u" }, csrf_token: currentCsrf });
        return;
      }
      if (req.method === "GET" && path === "/auth/me") {
        if (!cookie) {
          sendJSON(res, 401, { ok: false, error: { code: "forbidden", message: "会话无效或缺失" } });
          return;
        }
        currentCsrf = `csrf-${observed.filter((entry) => entry.path === "/auth/me").length + 1}`;
        sendJSON(res, 200, { user: { id: "u1", username: "u" }, csrf_token: currentCsrf });
        return;
      }
      if (req.method === "POST" && path.startsWith("/files/blob/reserve")) {
        if (sessionRejected(req, cookie, csrf)) {
          sendJSON(res, 403, { ok: false, error: { code: "forbidden", message: "CSRF 校验失败" } });
          return;
        }
        handled.reserve += 1;
        blobSeq += 1;
        const name = new URL(path, "http://x").searchParams.get("name") || "target.bin";
        // 服务端契约: path 是不含目录布局的相对 id/name, 不泄露服务器绝对路径。
        const record: BlobRecord = { id: `blob-${blobSeq}`, path: `blob-${blobSeq}/${name}`, body: "" };
        stagedBlobs.set(record.id, record);
        sendJSON(res, 200, { data: { id: record.id, path: record.path } });
        return;
      }
      if (req.method === "POST" && path.startsWith("/files/blob")) {
        if (blobPostForbidden) {
          sendJSON(res, 403, { ok: false, error: { code: "forbidden", message: "没有权限" } });
          return;
        }
        if (sessionRejected(req, cookie, csrf)) {
          sendJSON(res, 403, { ok: false, error: { code: "forbidden", message: "CSRF 校验失败" } });
          return;
        }
        handled.stage += 1;
        blobSeq += 1;
        const name = new URL(path, "http://x").searchParams.get("name") || "upload.bin";
        const record: BlobRecord = { id: `blob-${blobSeq}`, path: `blob-${blobSeq}/${name}`, body };
        stagedBlobs.set(record.id, record);
        sendJSON(res, 200, { data: { id: record.id, path: record.path, bytes: body.length } });
        return;
      }
      if (req.method === "GET" && path.startsWith("/files/blob")) {
        const id = new URL(path, "http://x").searchParams.get("id") ?? "";
        const record = stagedBlobs.get(id);
        if (!record) {
          sendJSON(res, 404, { ok: false, error: { code: "not_found", message: "暂存文件不存在或已过期" } });
          return;
        }
        res.writeHead(200, { "content-type": "application/octet-stream" });
        res.end(record.body);
        return;
      }
      if (req.method === "DELETE" && path.startsWith("/files/blob")) {
        if (sessionRejected(req, cookie, csrf)) {
          sendJSON(res, 403, { ok: false, error: { code: "forbidden", message: "CSRF 校验失败" } });
          return;
        }
        handled.remove += 1;
        const id = new URL(path, "http://x").searchParams.get("id") ?? "";
        const existed = stagedBlobs.delete(id);
        res.writeHead(existed ? 204 : 404);
        res.end();
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
  handled.stage = 0;
  handled.reserve = 0;
  handled.remove = 0;
  stagedBlobs.clear();
  blobSeq = 0;
  jarCookie = "";
  currentCsrf = "csrf-1";
  alwaysCsrfFail = false;
  blobPostForbidden = false;
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

function blobCalls(): ObservedRequest[] {
  return observed.filter((entry) => entry.path.startsWith("/files/blob"));
}

function meCalls(): ObservedRequest[] {
  return observed.filter((entry) => entry.path === "/auth/me");
}

function pngBytes() {
  return new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
}

describe("webFiles blob 传输的会话/CSRF 契约", () => {
  it("已登录会话 stageFile 携带 X-NexTerm-CSRF 成功暂存", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "a.png", { type: "image/png" }));

    expect(ref.id).toBe("blob-1");
    expect(ref.path).toContain("a.png");
    expect(blobCalls()).toHaveLength(1);
    expect(blobCalls()[0]?.csrf).toBe("csrf-1");
    expect(handled.stage).toBe(1);
  });

  it("authApi.login 建立会话后可直接暂存(复用 M125 登录 API)", async () => {
    const session = await authApi.login("u", "p");
    if (isMfaChallenge(session)) throw new Error("unexpected MFA challenge");
    // 会话建立后由调用方记住 CSRF 令牌(login 可能返回 MFA 挑战,不能代记)
    setCsrfToken(session.csrf_token);

    expect(session.user.username).toBe("u");
    expect(getCsrfToken()).toBe("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "b.png", { type: "image/png" }));

    expect(ref.id.length).toBeGreaterThan(0);
    const upload = blobCalls();
    expect(upload).toHaveLength(1);
    expect(upload[0]?.cookie).toBe("nexterm_session=s-1");
    expect(upload[0]?.csrf).toBe("csrf-1");
  });

  it("无会话(loopback/auth=off)不携带 CSRF 头且请求照常", async () => {
    const ref = await stageFile(new File([pngBytes()], "c.png", { type: "image/png" }));

    expect(ref.id.length).toBeGreaterThan(0);
    expect(blobCalls()).toHaveLength(1);
    expect(blobCalls()[0]?.cookie).toBeNull();
    expect(blobCalls()[0]?.csrf).toBeNull();
    expect(handled.stage).toBe(1);
  });

  it("stageFile 令牌过期: 精确 CSRF 403 时经 /auth/me 刷新并重试一次, 上传不重复", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-stale");

    const ref = await stageFile(new File([pngBytes()], "d.png", { type: "image/png" }));

    expect(ref.id).toBe("blob-1");
    const uploads = blobCalls();
    expect(uploads).toHaveLength(2);
    expect(meCalls()).toHaveLength(1);
    expect(uploads[0]?.csrf).toBe("csrf-stale");
    expect(uploads[1]?.csrf).toBe("csrf-2");
    expect(uploads[1]?.body).toBe(uploads[0]?.body);
    // 首次请求被中间件拒绝, handler 只执行一次, 不产生重复暂存。
    expect(handled.stage).toBe(1);
    expect(getCsrfToken()).toBe("csrf-2");
  });

  it("stageFile 重试后仍是 CSRF 403 时不再重试", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-stale");
    alwaysCsrfFail = true;

    const failure = await stageFile(new File([pngBytes()], "e.png", { type: "image/png" })).catch(
      (e: unknown) => e,
    );

    expect((failure as Error).message).toContain("HTTP 403");
    expect(blobCalls()).toHaveLength(2);
    expect(meCalls()).toHaveLength(1);
    expect(handled.stage).toBe(0);
  });

  it("stageFile 非 CSRF 的 403 不重试也不刷新会话", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");
    blobPostForbidden = true;

    const failure = await stageFile(new File([pngBytes()], "f.png", { type: "image/png" })).catch(
      (e: unknown) => e,
    );

    expect((failure as Error).message).toContain("HTTP 403");
    expect(blobCalls()).toHaveLength(1);
    expect(meCalls()).toHaveLength(0);
    expect(handled.stage).toBe(0);
  });

  it("reserveTarget 与 deleteBlob 携带会话 CSRF 头, 预留后可删除", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "g.png", { type: "image/png" }), { persist: true });
    expect(ref.id.length).toBeGreaterThan(0);

    // requestSaveTarget 在无 showSaveFilePicker 的环境下先预留落点再返回 path。
    const reservedPath = await requestSaveTarget("target.bin");
    expect(reservedPath).toContain("target.bin");
    const reserve = observed.filter((entry) => entry.path.startsWith("/files/blob/reserve"));
    expect(reserve).toHaveLength(1);
    expect(reserve[0]?.csrf).toBe("csrf-1");
    expect(handled.reserve).toBe(1);

    await dropStaged(reservedPath ?? "");
    const deletion = blobCalls().filter((entry) => entry.method === "DELETE");
    expect(deletion).toHaveLength(1);
    expect(deletion[0]?.csrf).toBe("csrf-1");
    expect(handled.remove).toBe(1);
    expect(stagedBlobs.size).toBe(1);
  });

  it("deleteBlob 令牌过期: 刷新重试一次, 删除只执行一次", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "h.png", { type: "image/png" }));
    expect(stagedBlobs.has(ref.id)).toBe(true);
    setCsrfToken("csrf-stale");

    await dropStaged(ref.path);

    const deletions = blobCalls().filter((entry) => entry.method === "DELETE");
    expect(deletions).toHaveLength(2);
    expect(meCalls()).toHaveLength(1);
    expect(deletions[0]?.csrf).toBe("csrf-stale");
    expect(deletions[1]?.csrf).toBe("csrf-2");
    // 首次删除被中间件拒绝, 实际删除只发生一次。
    expect(handled.remove).toBe(1);
    expect(stagedBlobs.has(ref.id)).toBe(false);
  });

  it("deleteBlob 非 CSRF 的失败保持静默(不抛出也不重试)", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "i.png", { type: "image/png" }));
    // 直接删掉服务端记录, 让随后的 DELETE 命中 404(非 CSRF 失败)。
    stagedBlobs.delete(ref.id);

    await expect(dropStaged(ref.path)).resolves.toBeUndefined();

    const deletions = blobCalls().filter((entry) => entry.method === "DELETE");
    expect(deletions).toHaveLength(1);
    expect(meCalls()).toHaveLength(0);
  });

  it("相对 id/name 落点: requestSaveTarget 预留后 deliverStaged 按 id 回读、写入手柄并删除暂存", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const writes: string[] = [];
    const handle = {
      createWritable: async () => ({
        write: async (data: Blob) => {
          writes.push(await data.text());
        },
        close: async () => undefined,
      }),
    };
    const win = window as unknown as Record<string, unknown>;
    win.showSaveFilePicker = vi.fn(async () => handle);
    try {
      const target = await requestSaveTarget("report.txt");
      // 服务端契约: 响应 path 是相对 id/name, 不含服务器目录布局。
      expect(target).toBe("blob-1/report.txt");

      // 服务端 fs_download 按暂存引用解析后把内容写进预留落点。
      const record = stagedBlobs.get("blob-1");
      if (!record) throw new Error("reserved blob missing");
      record.body = "downloaded-bytes";

      await expect(deliverStaged(target ?? "")).resolves.toBe("delivered");
      expect(writes).toEqual(["downloaded-bytes"]);
      expect(stagedBlobs.has("blob-1")).toBe(false);
      const deletions = blobCalls().filter((entry) => entry.method === "DELETE");
      expect(deletions).toHaveLength(1);
      expect(deletions[0]?.path).toContain("id=blob-1");
    } finally {
      delete win.showSaveFilePicker;
    }
  });
});
