/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ConnectTemplateInput } from "../../features/explorer/connectTemplates";

const KEY = "nexterm.connectTemplates.v1";

function validInput(): ConnectTemplateInput {
  return {
    name: "生产模板",
    kind: "ssh",
    port: 2222,
    username: "deploy",
    authKind: "key",
    keyOrigin: "vault",
    keyPath: "",
    vaultCredId: "cred-9",
    jumpAssetId: "",
    proxyCommand: "",
    forwardAgent: false,
    agentSocket: "",
    certPath: "",
    startupCommand: "htop",
    encoding: "gbk",
    envText: "LANG=C",
  };
}

async function loadStore() {
  return (await import("../../features/explorer/connectTemplates")).useConnectTemplates;
}

beforeEach(() => {
  localStorage.clear();
  vi.resetModules();
});

describe("connectTemplates", () => {
  it("add 追加并持久化, remove 删除并持久化", async () => {
    const store = await loadStore();
    const created = store.getState().add(validInput());
    expect(created.id).toBeTruthy();
    expect(store.getState().templates.map((t) => t.name)).toEqual(["生产模板"]);
    const persisted = JSON.parse(localStorage.getItem(KEY) ?? "[]") as Record<string, unknown>[];
    expect(persisted).toHaveLength(1);
    expect(persisted[0]).toMatchObject({
      id: created.id,
      name: "生产模板",
      kind: "ssh",
      port: 2222,
      username: "deploy",
      authKind: "key",
      keyOrigin: "vault",
      vaultCredId: "cred-9",
      startupCommand: "htop",
      encoding: "gbk",
      envText: "LANG=C",
    });
    expect(persisted[0]).not.toHaveProperty("host");

    store.getState().remove(created.id);
    expect(store.getState().templates).toEqual([]);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "[]")).toEqual([]);
  });

  it("remove 不存在的 id 不动存储", async () => {
    const store = await loadStore();
    const created = store.getState().add(validInput());
    const before = localStorage.getItem(KEY);
    store.getState().remove("tpl-missing");
    expect(store.getState().templates.map((t) => t.id)).toEqual([created.id]);
    expect(localStorage.getItem(KEY)).toBe(before);
  });

  it("启动时清洗非法条目并补默认值", async () => {
    localStorage.setItem(
      KEY,
      JSON.stringify([
        null,
        "junk",
        { name: "缺 id" },
        { id: "x", name: "坏类型", kind: "telnet" },
        { id: "t1", name: "完整", kind: "ssh" },
      ]),
    );
    const store = await loadStore();
    expect(store.getState().templates).toEqual([
      expect.objectContaining({
        id: "t1",
        name: "完整",
        kind: "ssh",
        port: 22,
        username: "",
        authKind: "password",
        keyOrigin: "ref",
        keyPath: "",
        vaultCredId: "",
        forwardAgent: false,
        encoding: "utf-8",
        envText: "",
      }),
    ]);
  });

  it("localStorage 损坏时按空列表处理", async () => {
    localStorage.setItem(KEY, "{not json");
    const store = await loadStore();
    expect(store.getState().templates).toEqual([]);
  });
});
