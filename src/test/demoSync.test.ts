import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

interface DemoBundle {
  protocol: number;
  origin: string;
  exportedAt: number;
  groups: Record<string, unknown>[];
  assets: Record<string, unknown>[];
  creds: Record<string, unknown>[];
  warnings: string[];
}

async function demoExport(assetIds: string[], withCreds: boolean): Promise<DemoBundle> {
  return (await mockInvoke("sync_export", { args: { assetIds, withCreds } })) as DemoBundle;
}

describe("demo 资产包 mock", () => {
  it("sync_digest 返回演示资产且不含内置资产", async () => {
    const digest = (await mockInvoke("sync_digest")) as { assets: { id: string }[] };
    const ids = digest.assets.map((a) => a.id);
    expect(ids).toContain("a-web01");
    expect(ids).not.toContain("a-local");
  });

  it("sync_export 含凭据时导出秘密，不含凭据时保留引用但不出秘密", async () => {
    const withCreds = await demoExport(["a-web01"], true);
    expect(withCreds.assets).toHaveLength(1);
    expect(withCreds.assets[0].credId).toBe("cred-web01");
    expect(withCreds.creds).toHaveLength(1);
    expect(withCreds.creds[0].id).toBe("cred-web01");
    expect(String(withCreds.creds[0].secret).length).toBeGreaterThan(0);
    expect(withCreds.groups.map((g) => g.id)).toContain("g-test");

    const withoutCreds = await demoExport(["a-web01"], false);
    expect(withoutCreds.creds).toHaveLength(0);
    expect(withoutCreds.assets[0].credId).toBe("cred-web01");
    expect(JSON.stringify(withoutCreds)).not.toContain("Xk9#web01$pw");
  });

  it("导出 → 导入 回环：本机已有条目计为更新，报告计数与生产语义一致", async () => {
    const bundle = await demoExport(["a-web01"], true);
    const report = (await mockInvoke("sync_import", { args: { bundle, force: false } })) as Record<
      string,
      number
    >;
    expect(report.assetsUpdated).toBe(1);
    expect(report.assetsCreated).toBe(0);
    expect(report.credsUpdated).toBe(1);
    expect(report.credsCreated).toBe(0);
    expect(report.groupsUpdated).toBe(1);
    expect(report.refused).toBe(0);
    expect(report.skippedNewer).toBe(0);
  });

  it("本机较新且未 force 时跳过，force 时覆盖", async () => {
    const bundle = await demoExport(["a-web01"], false);
    bundle.assets[0].updatedAt = 1;
    const skipped = (await mockInvoke("sync_import", { args: { bundle, force: false } })) as Record<
      string,
      number
    >;
    expect(skipped.skippedNewer).toBe(1);
    expect(skipped.assetsUpdated).toBe(0);

    const forced = (await mockInvoke("sync_import", { args: { bundle, force: true } })) as Record<
      string,
      number
    >;
    expect(forced.assetsUpdated).toBe(1);
    expect(forced.skippedNewer).toBe(0);
  });

  it("协议版本不符或结构非法时按生产错误码拒绝", async () => {
    await expect(
      mockInvoke("sync_import", { args: { bundle: { protocol: 99, groups: [], assets: [], creds: [] } } }),
    ).rejects.toMatchObject({ code: "unsupported" });
    await expect(mockInvoke("sync_import", { args: { bundle: "junk" } })).rejects.toMatchObject({
      code: "bad_param",
    });
  });

  it("凭据库锁定时含凭据的导入被拒绝，解锁后放行", async () => {
    const bundle = await demoExport(["a-web01"], true);
    await mockInvoke("vault_lock");
    await expect(
      mockInvoke("sync_import", { args: { bundle, force: false } }),
    ).rejects.toMatchObject({ code: "vault_locked" });
    await mockInvoke("vault_unlock");
    const report = (await mockInvoke("sync_import", { args: { bundle, force: false } })) as Record<
      string,
      number
    >;
    expect(report.assetsUpdated).toBe(1);
  });

  it("导入新资产后本机 digest 立即可见", async () => {
    const bundle: DemoBundle = {
      protocol: 1,
      origin: "other",
      exportedAt: Date.now(),
      groups: [],
      assets: [
        {
          id: "a-imported",
          groupId: null,
          kind: "ssh",
          name: "imported-01",
          host: "10.9.9.9",
          port: 22,
          username: "root",
          authKind: "password",
          keyPath: null,
          credId: "cred-missing",
          optionsJson: "{}",
          tags: "",
          note: "",
          sort: 0,
          createdAt: 1,
          updatedAt: Date.now(),
          deletedAt: null,
        },
      ],
      creds: [],
      warnings: [],
    };
    const report = (await mockInvoke("sync_import", { args: { bundle, force: false } })) as Record<
      string,
      number | string[]
    >;
    expect(report.assetsCreated).toBe(1);
    expect((report.warnings as string[]).join("\n")).toContain("已清除该引用");
    const digest = (await mockInvoke("sync_digest")) as { assets: { id: string }[] };
    expect(digest.assets.map((a) => a.id)).toContain("a-imported");
  });

  it("本机较新时关联凭据不被包内旧值覆盖", async () => {
    const newer: DemoBundle = {
      protocol: 1,
      origin: "other",
      exportedAt: Date.now(),
      groups: [],
      assets: [
        {
          id: "a-web01",
          groupId: null,
          kind: "ssh",
          name: "web-01",
          host: "127.0.0.1",
          port: 22,
          username: "deploy",
          authKind: "password",
          keyPath: null,
          credId: "cred-web01",
          optionsJson: "{}",
          tags: "",
          note: "",
          sort: 0,
          createdAt: 1,
          updatedAt: 2_000_000_000_000,
          deletedAt: null,
        },
      ],
      creds: [{ id: "cred-web01", name: "web-01", kind: "password", secret: "new-local-value" }],
      warnings: [],
    };
    const first = (await mockInvoke("sync_import", { args: { bundle: newer, force: false } })) as Record<
      string,
      number
    >;
    expect(first.assetsUpdated).toBe(1);
    expect(first.credsUpdated).toBe(1);

    const older: DemoBundle = {
      ...newer,
      assets: [{ ...newer.assets[0], updatedAt: 1_000_000_000_000 }],
      creds: [{ id: "cred-web01", name: "web-01", kind: "password", secret: "stale-value" }],
    };
    const second = (await mockInvoke("sync_import", { args: { bundle: older, force: false } })) as Record<
      string,
      number | string[]
    >;
    expect(second.skippedNewer).toBe(1);
    expect(second.assetsUpdated).toBe(0);
    expect(second.credsUpdated).toBe(0);
    expect(second.credsCreated).toBe(0);
    expect((second.warnings as string[]).join("\n")).toContain("受到保护");
    const revealed = (await mockInvoke("vault_reveal_credential", { args: { id: "cred-web01" } })) as {
      value: string;
    };
    expect(revealed.value).toBe("new-local-value");
  });

  it("本机较新的删除标记不被包内 live 行复活", async () => {
    await mockInvoke("asset_delete", { args: { id: "a-web01" } });
    const bundle: DemoBundle = {
      protocol: 1,
      origin: "other",
      exportedAt: Date.now(),
      groups: [],
      assets: [
        {
          id: "a-web01",
          groupId: null,
          kind: "ssh",
          name: "web-01",
          host: "127.0.0.1",
          port: 22,
          username: "deploy",
          authKind: "password",
          keyPath: null,
          credId: null,
          optionsJson: "{}",
          tags: "",
          note: "",
          sort: 0,
          createdAt: 1,
          updatedAt: Date.now() - 60_000,
          deletedAt: null,
        },
      ],
      creds: [],
      warnings: [],
    };
    const report = (await mockInvoke("sync_import", { args: { bundle, force: false } })) as Record<
      string,
      number
    >;
    expect(report.skippedNewer).toBe(1);
    expect(report.assetsUpdated).toBe(0);
    const digest = (await mockInvoke("sync_digest")) as {
      assets: { id: string; deletedAt: number | null }[];
    };
    expect(digest.assets.find((a) => a.id === "a-web01")?.deletedAt).not.toBeNull();
  });

  it("内置资产被拒绝时其关联凭据保持不变", async () => {
    const before = (await mockInvoke("vault_reveal_credential", { args: { id: "cred-web01" } })) as {
      value: string;
    };
    const bundle: DemoBundle = {
      protocol: 1,
      origin: "other",
      exportedAt: Date.now(),
      groups: [],
      assets: [
        {
          id: "a-local",
          groupId: null,
          kind: "local",
          name: "伪造本地",
          host: null,
          port: null,
          username: null,
          authKind: "none",
          keyPath: null,
          credId: "cred-web01",
          optionsJson: "{}",
          tags: "",
          note: "",
          sort: 0,
          createdAt: 1,
          updatedAt: Date.now(),
          deletedAt: null,
        },
      ],
      creds: [{ id: "cred-web01", name: "web-01", kind: "password", secret: "attacker-value" }],
      warnings: [],
    };
    const report = (await mockInvoke("sync_import", { args: { bundle, force: false } })) as Record<
      string,
      number | string[]
    >;
    expect(report.refused).toBe(1);
    expect(report.assetsUpdated).toBe(0);
    expect(report.credsUpdated).toBe(0);
    expect(report.credsCreated).toBe(0);
    expect((report.warnings as string[]).join("\n")).toContain("受到保护");
    const after = (await mockInvoke("vault_reveal_credential", { args: { id: "cred-web01" } })) as {
      value: string;
    };
    expect(after.value).toBe(before.value);
  });
});
