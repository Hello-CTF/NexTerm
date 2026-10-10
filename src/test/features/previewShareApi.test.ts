/** @vitest-environment jsdom */
// 只读预览 API 线上契约: 路径/方法/CSRF/蛇形 JSON 字段对齐
// internal/server/preview_http.go 的真实路由; 创建响应是唯一携带一次性 token
// 的入口, token 只用于当场拼公开 URL。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
  };
});

interface CapturedRequest {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
}

function capture(): CapturedRequest[] {
  const calls: CapturedRequest[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    calls.push({
      url: String(url),
      method: init.method ?? "GET",
      headers: (init.headers ?? {}) as Record<string, string>,
      ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ ok: true }),
    };
  });
  return calls;
}

import { createPreviewLink, listPreviewLinks, previewPublicUrl, previewWsUrl, revokePreviewLink } from "../../features/sharing/previewApi";
import { setCsrfToken } from "../../ipc/authApi";

beforeEach(() => {
  vi.clearAllMocks();
  setCsrfToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("previewApi 线上契约", () => {
  it("创建预览链接走 POST /share/previews, 蛇形字段 + CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await createPreviewLink({ sessionId: "tab-1", ttlMs: 3_600_000 });

    expect(calls[0]).toMatchObject({
      url: "/share/previews",
      method: "POST",
      body: { session_id: "tab-1", ttl_ms: 3_600_000 },
    });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("预览列表走 GET /share/previews 且不带 CSRF 头", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await listPreviewLinks();

    expect(calls[0]).toMatchObject({ url: "/share/previews", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("吊销预览链接走 POST /share/previews/{id}/revoke, 路径编码且带 CSRF, 无请求体", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await revokePreviewLink("id/with space");

    expect(calls[0]).toMatchObject({ url: "/share/previews/id%2Fwith%20space/revoke", method: "POST" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[0]?.body).toBeUndefined();
  });

  it("previewPublicUrl 拼当前源的公开地址", () => {
    expect(previewPublicUrl("tok-1")).toBe(`${window.location.origin}/share/preview/tok-1`);
  });

  it("previewWsUrl 对 token 做 URI 编码", () => {
    const url = previewWsUrl("abc/123 ?=");
    expect(url).toContain("/share/preview/abc%2F123%20%3F%3D");
  });
});
