import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

function installWebEnv() {
  const storage = new Map<string, string>();
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "", protocol: "http:", host: "127.0.0.1:9" },
    localStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, value),
    },
  });
}

function captureRpc() {
  const calls: { url: unknown; cmd?: unknown; args?: unknown }[] = [];
  vi.stubGlobal("fetch", async (...fetchArgs: unknown[]) => {
    const init = fetchArgs[1] as { body?: string } | undefined;
    const body = JSON.parse(String(init?.body)) as { cmd?: unknown; args?: unknown };
    calls.push({ url: fetchArgs[0], ...body });
    return { text: async () => JSON.stringify({ ok: true, data: null }) };
  });
  return calls;
}

describe("sync collection read wire contract", () => {
  beforeEach(() => {
    vi.resetModules();
    installWebEnv();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("collectAssets wraps args and always sends includeDeleted", async () => {
    const calls = captureRpc();
    const { syncApi } = await import("../ipc/commands");

    await syncApi.collectAssets(true, "01ARZ3NDEKTSV4RRFFQ69G5FAV", 64);
    await syncApi.collectAssets(false);

    expect(calls).toEqual([
      {
        url: "/rpc",
        cmd: "sync_collect_assets",
        args: { args: { includeDeleted: true, afterId: "01ARZ3NDEKTSV4RRFFQ69G5FAV", limit: 64 } },
      },
      { url: "/rpc", cmd: "sync_collect_assets", args: { args: { includeDeleted: false } } },
    ]);
  });

  it("passes collectTombstones pagination fields directly", async () => {
    const calls = captureRpc();
    const { syncApi } = await import("../ipc/commands");

    await syncApi.collectTombstones("01ARZ3NDEKTSV4RRFFQ69G5FAV", 32);

    expect(calls).toEqual([
      {
        url: "/rpc",
        cmd: "sync_collect_tombstones",
        args: { args: { afterId: "01ARZ3NDEKTSV4RRFFQ69G5FAV", limit: 32 } },
      },
    ]);
  });

  it("collectCredentials always sends revealSecrets and exact pagination fields", async () => {
    const calls = captureRpc();
    const { syncApi } = await import("../ipc/commands");

    await syncApi.collectCredentials(true, undefined, 16);

    expect(calls).toEqual([
      {
        url: "/rpc",
        cmd: "sync_collect_credentials",
        args: { args: { revealSecrets: true, limit: 16 } },
      },
    ]);
  });
});
