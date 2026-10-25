import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface Pending {
  cmd: string;
  args: unknown;
  respond: (body: unknown) => void;
}

// Each fetch stays pending until the test answers it, so overlap is deterministic.
function deferredRpc() {
  const pending: Pending[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (_url: string, init: RequestInit) =>
        new Promise((resolve) => {
          const sent = JSON.parse(String(init.body)) as { cmd: string; args: unknown };
          pending.push({
            cmd: sent.cmd,
            args: sent.args,
            respond: (body) =>
              resolve({ ok: true, status: 200, text: async () => JSON.stringify(body) }),
          });
        }),
    ),
  );
  return pending;
}

async function load() {
  const commands = await import("../ipc/commands");
  const authApi = await import("../ipc/authApi");
  const epoch = await import("../ipc/authSessionEpoch");
  authApi.setCsrfToken("csrf");
  return { commands, epoch };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", { __NEXTERM_TRANSPORT__: "web", location: { search: "" } });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("web /rpc in-flight read sharing", () => {
  it("sends one request for concurrent identical reads and gives each waiter its own copy", async () => {
    const pending = deferredRpc();
    const { commands } = await load();

    const first = commands.assetApi.list();
    const second = commands.assetApi.list();
    await flush();
    expect(pending).toHaveLength(1);
    pending[0].respond({ ok: true, data: [{ id: "a" }] });

    const [a, b] = (await Promise.all([first, second])) as unknown as { id: string }[][];
    expect(a).toEqual([{ id: "a" }]);
    expect(a).not.toBe(b);
    a[0].id = "mutated";
    expect(b[0].id).toBe("a");
  });

  it("still gives each waiter an independent copy where structuredClone does not exist", async () => {
    vi.stubGlobal("structuredClone", undefined);
    const pending = deferredRpc();
    const { commands } = await load();

    const first = commands.assetApi.list();
    const second = commands.assetApi.list();
    await flush();
    expect(pending).toHaveLength(1);
    pending[0].respond({ ok: true, data: [{ id: "a" }] });

    const [a, b] = (await Promise.all([first, second])) as unknown as { id: string }[][];
    expect(a).toEqual([{ id: "a" }]);
    expect(a).not.toBe(b);
    a[0].id = "mutated";
    expect(b[0].id).toBe("a");
  });

  it("does not share reads with different args or different commands", async () => {
    const pending = deferredRpc();
    const { commands } = await load();

    void commands.assetApi.get("a");
    void commands.assetApi.get("b");
    void commands.assetApi.list();
    await flush();

    expect(pending.map((p) => p.cmd)).toEqual(["asset_get", "asset_get", "asset_list"]);
  });

  it("rejects every waiter when the shared request fails", async () => {
    const pending = deferredRpc();
    const { commands } = await load();

    const first = commands.assetApi.list();
    const second = commands.assetApi.list();
    await flush();
    pending[0].respond({ ok: false, error: { code: "internal", message: "boom" } });

    await expect(first).rejects.toMatchObject({ code: "internal", message: "boom" });
    await expect(second).rejects.toMatchObject({ code: "internal", message: "boom" });
    void commands.assetApi.list();
    await flush();
    expect(pending).toHaveLength(2);
  });

  it("does not deliver a result across an auth session epoch change", async () => {
    const pending = deferredRpc();
    const { commands, epoch } = await load();

    const before = commands.assetApi.list();
    await flush();
    epoch.advanceAuthSessionEpoch();
    const after = commands.assetApi.list();
    await flush();
    expect(pending).toHaveLength(2);

    pending[0].respond({ ok: true, data: [{ id: "old-account" }] });
    pending[1].respond({ ok: true, data: [{ id: "new-account" }] });
    await expect(before).rejects.toMatchObject({ code: "account_changed" });
    await expect(after).resolves.toEqual([{ id: "new-account" }]);
  });

  it("never deduplicates writes and does not let a read started after a write join an earlier read", async () => {
    const pending = deferredRpc();
    const { commands } = await load();

    void commands.assetApi.list();
    await flush();
    void commands.assetApi.delete("a");
    void commands.assetApi.delete("a");
    void commands.assetApi.list();
    await flush();

    expect(pending.map((p) => p.cmd)).toEqual(["asset_list", "asset_delete", "asset_delete", "asset_list"]);
  });
});
