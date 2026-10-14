import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SyncApplyObject } from "../ipc/types";

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
    return {
      text: async () =>
        JSON.stringify({
          ok: true,
          data: { applied: 1, identical: 0, skipped: 0, objects: [] },
        }),
    };
  });
  return calls;
}

describe("sync_apply_objects wire contract", () => {
  beforeEach(() => {
    vi.resetModules();
    installWebEnv();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("applyObjects wraps args and preserves exact object fields", async () => {
    const calls = captureRpc();
    const { syncApi } = await import("../ipc/commands");
    const objects: SyncApplyObject[] = [
      {
        id: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
        kind: "group",
        payload: { id: "01ARZ3NDEKTSV4RRFFQ69G5FAV", name: "Synced group", sort: 0, createdAt: 1, updatedAt: 100 },
      },
      {
        id: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
        kind: "tombstone",
        payload: { targetKind: "group", deletedAt: 200 },
      },
    ];

    const result = await syncApi.applyObjects(objects);

    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toBe("/rpc");
    expect(calls[0]?.cmd).toBe("sync_apply_objects");
    expect(calls[0]?.args).toEqual({ args: { objects } });
    expect(result).toEqual({ applied: 1, identical: 0, skipped: 0, objects: [] });
  });
});
