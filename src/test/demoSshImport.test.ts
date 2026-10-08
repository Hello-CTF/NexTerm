import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

interface DemoAssetRow {
  id: string;
  kind: string;
  name: string;
  host: string | null;
  port: number | null;
  username: string | null;
  authKind: string | null;
  credId: string | null;
}

interface DemoCredentialRow {
  id: string;
  name: string;
  kind: string;
  source: string | null;
  refPath: string | null;
}

async function expectErrorCode(promise: Promise<unknown>, code: string): Promise<void> {
  try {
    await promise;
    expect.unreachable(`expected ${code} error`);
  } catch (e) {
    expect(e).toMatchObject({ code });
  }
}

describe("demo SSH 导入", () => {
  it("ssh-config 预览返回罐头数据且不包含私钥材料", async () => {
    const preview = (await mockInvoke("ssh_import_preview", {
      args: { source: "ssh-config" },
    })) as Record<string, unknown>;
    const hosts = preview.hosts as { alias: string; action: string }[];
    const keys = preview.keys as { aliases: string[]; action: string }[];
    expect(hosts.map((h) => h.action)).toEqual([
      "add",
      "add",
      "skip-duplicate",
      "conflict-alias",
    ]);
    expect(keys.map((k) => k.aliases[0])).toEqual(["id_rsa_demo", "id_ed25519"]);
    expect(JSON.stringify(preview)).not.toContain("PRIVATE KEY");
  });

  it("Termius 预览必须显式确认", async () => {
    await expectErrorCode(
      mockInvoke("ssh_import_preview", { args: { source: "termius" } }),
      "bad_param",
    );
    const preview = (await mockInvoke("ssh_import_preview", {
      args: { source: "termius", confirmed: true },
    })) as { source: string; hosts: { alias: string }[] };
    expect(preview.source).toBe("termius");
    expect(preview.hosts[0]?.alias).toBe("termius-prod");
  });

  it("Termius 完全同名主机以稳定 ID 区分并保留所有行", async () => {
    const preview = (await mockInvoke("ssh_import_preview", {
      args: { source: "termius", confirmed: true },
    })) as { hosts: { id: string; alias: string; action: string }[] };
    const sameName = preview.hosts.filter((h) => h.alias === "termius-prod");
    expect(sameName).toHaveLength(2);
    expect(sameName[0]!.id).not.toBe(sameName[1]!.id);
    expect(sameName.map((h) => h.action)).toEqual(["add", "conflict-alias"]);

    const result = (await mockInvoke("ssh_import_apply", {
      args: {
        source: "termius",
        confirmed: true,
        hosts: [
          { id: sameName[0]!.id, action: "import" },
          { id: sameName[1]!.id, action: "skip" },
        ],
        keys: [{ id: "k0", action: "skip" }],
      },
    })) as Record<string, number>;
    expect(result.assetsCreated).toBe(1);
    expect(result.skipped).toBe(3);

    const assets = (await mockInvoke("asset_list")) as DemoAssetRow[];
    const imported = assets.filter((a) => a.name === "termius-prod");
    expect(imported).toHaveLength(1);
    expect(imported[0]!.host).toBe("prod.demo.internal");
    expect(imported[0]!.authKind).toBe("key");
    expect(imported[0]!.credId).toBeNull();
  });

  it("apply 导入主机与密钥并写入 demo 数据", async () => {
    const before = (await mockInvoke("asset_list")) as DemoAssetRow[];
    const result = (await mockInvoke("ssh_import_apply", {
      args: {
        source: "ssh-config",
        hosts: [
          { id: "h0", action: "import" },
          { id: "h1", action: "import" },
          { id: "h2", action: "import" },
          { id: "h3", action: "skip" },
        ],
        keys: [{ id: "k0", action: "import" }],
      },
    })) as Record<string, number>;
    expect(result.assetsCreated).toBe(2);
    expect(result.credentialsCreated).toBe(1);
    expect(result.skipped).toBe(3);

    const after = (await mockInvoke("asset_list")) as DemoAssetRow[];
    expect(after.length).toBe(before.length + 2);
    const web02 = after.find((a) => a.name === "web-02");
    expect(web02?.host).toBe("web-02.demo.internal");
    expect(web02?.authKind).toBe("key");
    expect(web02?.credId).toBeTruthy();

    const creds = (await mockInvoke("vault_list_credentials")) as DemoCredentialRow[];
    const imported = creds.find((c) => c.name === "id_rsa_demo");
    expect(imported?.source).toBe("file");
    expect(imported?.refPath).toBe("/home/demo/.ssh/id_rsa_demo");
  });

  it("apply 覆盖同名资产并跳过端点冲突", async () => {
    const result = (await mockInvoke("ssh_import_apply", {
      args: {
        source: "ssh-config",
        hosts: [
          { id: "h3", action: "overwrite" },
          { id: "h2", action: "overwrite" },
        ],
        keys: [],
      },
    })) as Record<string, number>;
    expect(result.assetsUpdated).toBe(1);
    expect(result.skipped).toBe(5);

    const assets = (await mockInvoke("asset_list")) as DemoAssetRow[];
    const nat = assets.find((a) => a.name === "nat-01");
    expect(nat?.host).toBe("nat-new.demo.internal");
  });

  it("vault 锁定时 apply 被拒绝", async () => {
    await mockInvoke("vault_lock");
    try {
      await expectErrorCode(
        mockInvoke("ssh_import_apply", {
          args: { source: "ssh-config", hosts: [], keys: [] },
        }),
        "vault_locked",
      );
    } finally {
      await mockInvoke("vault_unlock");
    }
  });
});

describe("demo 生成密钥", () => {
  it("生成成功返回指纹与公钥行并入库", async () => {
    const generated = (await mockInvoke("vault_generate_key", {
      args: { name: "demo-gen-key", algorithm: "ed25519", passphrase: "pw" },
    })) as Record<string, unknown>;
    expect(generated.id).toBeTruthy();
    expect(generated.algorithm).toBe("ed25519");
    expect(String(generated.fingerprint)).toContain("SHA256:");
    expect(String(generated.publicKey)).toContain("ssh-ed25519");

    const creds = (await mockInvoke("vault_list_credentials")) as DemoCredentialRow[];
    const stored = creds.find((c) => c.name === "demo-gen-key");
    expect(stored?.source).toBe("inline");
  });

  it("空名称与重名被拒绝", async () => {
    await expectErrorCode(mockInvoke("vault_generate_key", { args: { name: "  " } }), "bad_param");
    await expectErrorCode(
      mockInvoke("vault_generate_key", { args: { name: "demo-gen-key" } }),
      "bad_param",
    );
  });
});

describe("demo SSH ~/.ssh 目录快速导入", () => {
  it("ssh-home 预览返回扫描结果、诊断且不含私钥材料", async () => {
    const preview = (await mockInvoke("ssh_import_preview", {
      args: { source: "ssh-home" },
    })) as Record<string, unknown>;
    expect(preview.source).toBe("ssh-home");
    expect(preview.path).toBe("/home/demo/.ssh");
    const hosts = preview.hosts as { alias: string; action: string }[];
    expect(hosts.map((h) => h.action)).toEqual(["add", "add", "skip-duplicate"]);
    const keys = preview.keys as { aliases: string[]; action: string }[];
    expect(keys.map((k) => k.aliases[0])).toEqual(["id_ed25519_home", "id_ed25519"]);
    expect(keys.map((k) => k.action)).toEqual(["add", "conflict-alias"]);
    const diagnostics = preview.diagnostics as { code: string }[];
    expect(diagnostics.map((d) => d.code)).toContain("unsupported-file");
    expect(JSON.stringify(preview)).not.toContain("PRIVATE KEY");
  });

  it("ssh-home 一键导入全部安全项：只提交安全项，新增项入库、冲突项不覆盖", async () => {
    const preview = (await mockInvoke("ssh_import_preview", {
      args: { source: "ssh-home" },
    })) as { hosts: { id: string; action: string }[]; keys: { id: string; action: string }[] };
    const result = (await mockInvoke("ssh_import_apply", {
      args: {
        source: "ssh-home",
        hosts: preview.hosts
          .filter((h) => h.action === "add")
          .map((h) => ({ id: h.id, action: "import" })),
        keys: preview.keys
          .filter((k) => k.action === "add")
          .map((k) => ({ id: k.id, action: "import" })),
      },
    })) as Record<string, number>;
    expect(result.assetsCreated).toBe(2);
    expect(result.credentialsCreated).toBe(1);
    expect(result.skipped).toBe(2);

    const creds = (await mockInvoke("vault_list_credentials")) as DemoCredentialRow[];
    const imported = creds.find((c) => c.name === "id_ed25519_home");
    expect(imported?.source).toBe("file");
    expect(imported?.refPath).toBe("/home/demo/.ssh/id_ed25519_home");
    const conflicted = creds.filter((c) => c.name === "id_ed25519");
    expect(conflicted).toHaveLength(1);

    const assets = (await mockInvoke("asset_list")) as DemoAssetRow[];
    const bastion = assets.find((a) => a.name === "demo-bastion");
    expect(bastion?.authKind).toBe("key");
    expect(bastion?.credId).toBe(imported?.id);
  });

  it("ssh-home 预览不需要 Termius 确认参数", async () => {
    await expectErrorCode(
      mockInvoke("ssh_import_preview", { args: { source: "termius" } }),
      "bad_param",
    );
    const preview = (await mockInvoke("ssh_import_preview", {
      args: { source: "ssh-home" },
    })) as { source: string };
    expect(preview.source).toBe("ssh-home");
  });
});
