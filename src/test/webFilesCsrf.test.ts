/** @vitest-environment jsdom */
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";

// Non-mock HTTP contract: replicate requireAuth session/CSRF semantics with a real node http server
// (POST/DELETE with a cookie session but a missing/wrong X-NexTerm-CSRF gets 403 with a CSRF validation error,
// and the rejection happens in middleware, the handler never runs), verifying stageFile/reserveTarget/deleteBlob
// carry the M125 account-session CSRF header, that an exact CSRF 403 refreshes via /auth/me and retries the same request once,
// other 403s are not retried, and uploads/deletes are not duplicated by the retry.
vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
});

import { deliverStaged, dropStaged, requestSaveTarget, stageFile } from "../ipc/webFiles";
import { authApi, getCsrfToken, isMfaChallenge, setCsrfToken } from "../ipc/authApi";

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
// Handler execution count is tracked separately from middleware rejections: a CSRF rejection must never reach the handler.
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

// Replicates requireAuth: no cookie is treated as loopback/auth=off and allowed through; with a cookie,
// POST/DELETE must carry a CSRF header matching the session, otherwise 403 without reaching the handler.
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
          sendJSON(res, 401, { ok: false, error: { code: "forbidden", message: "Session is invalid or missing" } });
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
        // Server contract: path is a relative id/name without directory layout, leaking no server absolute path.
        const record: BlobRecord = { id: `blob-${blobSeq}`, path: `blob-${blobSeq}/${name}`, body: "" };
        stagedBlobs.set(record.id, record);
        sendJSON(res, 200, { data: { id: record.id, path: record.path } });
        return;
      }
      if (req.method === "POST" && path.startsWith("/files/blob")) {
        if (blobPostForbidden) {
          sendJSON(res, 403, { ok: false, error: { code: "forbidden", message: "Permission denied" } });
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
          sendJSON(res, 404, { ok: false, error: { code: "not_found", message: "Staged file does not exist or has expired" } });
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

// vitest config unstubGlobals: true auto-unstubs before each test, so the cookie jar wrapper goes in beforeEach.
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

describe("webFiles blob transfer session/CSRF contract", () => {
  it("stageFile sends X-NexTerm-CSRF for a signed-in session and stages successfully", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "a.png", { type: "image/png" }));

    expect(ref.id).toBe("blob-1");
    expect(ref.path).toContain("a.png");
    expect(blobCalls()).toHaveLength(1);
    expect(blobCalls()[0]?.csrf).toBe("csrf-1");
    expect(handled.stage).toBe(1);
  });

  it("stageFile works immediately after authApi.login establishes a session (reusing the M125 login API)", async () => {
    const session = await authApi.login("u", "p");
    if (isMfaChallenge(session)) throw new Error("unexpected MFA challenge");
    // The caller remembers the CSRF token once the session is established (login may return an MFA challenge, so it cannot be remembered on the caller's behalf)
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

  it("sends requests without a CSRF header when there is no session (loopback/auth=off)", async () => {
    const ref = await stageFile(new File([pngBytes()], "c.png", { type: "image/png" }));

    expect(ref.id.length).toBeGreaterThan(0);
    expect(blobCalls()).toHaveLength(1);
    expect(blobCalls()[0]?.cookie).toBeNull();
    expect(blobCalls()[0]?.csrf).toBeNull();
    expect(handled.stage).toBe(1);
  });

  it("stageFile with an expired token refreshes through /auth/me on an exact CSRF 403 and retries once without duplicating the upload", async () => {
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
    // The first request was rejected by middleware, the handler ran only once, no duplicate staging.
    expect(handled.stage).toBe(1);
    expect(getCsrfToken()).toBe("csrf-2");
  });

  it("stageFile does not retry again when the retry still returns CSRF 403", async () => {
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

  it("stageFile does not retry or refresh the session for non-CSRF 403 responses", async () => {
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

  it("reserveTarget and deleteBlob send the session CSRF header, and reserved blobs can be deleted", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "g.png", { type: "image/png" }), { persist: true });
    expect(ref.id.length).toBeGreaterThan(0);

    // requestSaveTarget reserves the landing spot first and returns the path in environments without showSaveFilePicker.
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

  it("deleteBlob refreshes and retries once on token expiry, but deletes only once", async () => {
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
    // The first delete was rejected by middleware, the actual delete happened only once.
    expect(handled.remove).toBe(1);
    expect(stagedBlobs.has(ref.id)).toBe(false);
  });

  it("deleteBlob stays silent for non-CSRF failures (no throw and no retry)", async () => {
    jarCookie = "nexterm_session=s-1";
    setCsrfToken("csrf-1");

    const ref = await stageFile(new File([pngBytes()], "i.png", { type: "image/png" }));
    // Delete the server record directly, so the following DELETE hits a 404 (not a CSRF failure).
    stagedBlobs.delete(ref.id);

    await expect(dropStaged(ref.path)).resolves.toBeUndefined();

    const deletions = blobCalls().filter((entry) => entry.method === "DELETE");
    expect(deletions).toHaveLength(1);
    expect(meCalls()).toHaveLength(0);
  });

  it("relative id/name target: requestSaveTarget reserves, then deliverStaged reads by ID, writes the handle, and deletes the staged blob", async () => {
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
      // Server contract: the response path is a relative id/name, no server directory layout.
      expect(target).toBe("blob-1/report.txt");

      // Server fs_download resolves the staged reference and writes the content into the reserved landing spot.
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
