/** @vitest-environment jsdom */
// Preview API live contract: paths/methods/CSRF/snake_case JSON fields align with
// the real routes in internal/server/preview_http.go; the creation response is the only entry carrying the one-time token,
// and the token is only used to assemble the public URL on the spot. The creation body always explicitly carries write to decide writability.
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

describe("previewApi live contract", () => {
  it("creates preview links via POST /share/previews with snake_case fields and CSRF, defaulting write to false", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await createPreviewLink({ sessionId: "tab-1", ttlMs: 3_600_000 });

    expect(calls[0]).toMatchObject({
      url: "/share/previews",
      method: "POST",
      body: { session_id: "tab-1", ttl_ms: 3_600_000, write: false },
    });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("includes write:true in the body when creating a writable preview link", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await createPreviewLink({ sessionId: "tab-1", ttlMs: 3_600_000, write: true });

    expect(calls[0]?.body).toMatchObject({ session_id: "tab-1", ttl_ms: 3_600_000, write: true });
  });

  it("lists preview links via GET /share/previews without a CSRF header", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await listPreviewLinks();

    expect(calls[0]).toMatchObject({ url: "/share/previews", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("revokes preview links via POST /share/previews/{id}/revoke with an encoded path and CSRF, without a request body", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await revokePreviewLink("id/with space");

    expect(calls[0]).toMatchObject({ url: "/share/previews/id%2Fwith%20space/revoke", method: "POST" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[0]?.body).toBeUndefined();
  });

  it("previewPublicUrl builds a public URL from the current origin", () => {
    expect(previewPublicUrl("tok-1")).toBe(`${window.location.origin}/share/preview/tok-1`);
  });

  it("previewWsUrl URI-encodes the token", () => {
    const url = previewWsUrl("abc/123 ?=");
    expect(url).toContain("/share/preview/abc%2F123%20%3F%3D");
  });
});
