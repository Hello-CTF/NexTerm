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

describe("authedFetch 401 handling", () => {
  it("returns the response unchanged when status is not 401", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    vi.stubGlobal("fetch", fetchMock);
    const auth = await import("../ipc/serverAuth");

    const response = await auth.authedFetch("/rpc", { method: "POST" });
    expect(response.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("dispatches the re-login event and returns 401", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(401, { ok: false, error: { code: "forbidden", message: "Session is invalid or missing" } }));
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

  it("does not retry automatically after 401", async () => {
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

describe("callWeb fails immediately on 401", () => {
  it("fails immediately without replaying when the first call returns 401", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(401, { ok: false, error: { code: "forbidden", message: "Session is invalid or missing" } }))
      .mockResolvedValueOnce(jsonResponse(200, { ok: true, data: "linux" }));
    vi.stubGlobal("fetch", fetchMock);
    const { systemApi } = await import("../ipc/commands");
    const { setCsrfToken } = await import("../ipc/authApi");
    setCsrfToken("csrf-1");

    await expect(systemApi.platform()).rejects.toMatchObject({ code: "forbidden" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("fixed WebSocket subprotocol", () => {
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

  it("offers only the input subprotocol on channels and only nexterm on events", async () => {
    const { newBinaryChannel, subscribeEvent } = await import("../ipc/webTransport");

    newBinaryChannel();
    expect(RecordingWebSocket.instances[0].protocols).toEqual(["nexterm.input"]);

    subscribeEvent("session://status", () => {});
    expect(RecordingWebSocket.instances[1].url).toContain("/ws/events");
    expect(RecordingWebSocket.instances[1].protocols).toEqual(["nexterm"]);
  });
});
