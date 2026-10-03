import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";

// 演示实现走 window.setTimeout 模拟 IPC 延迟；node 环境下把全局定时器暴露成 window。
beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

const scope = { tenant: "local", subject: "default" };
const otherScope = { tenant: "local", subject: "other" };

interface DemoEntry {
  id: string;
  topic: string;
  content: string;
  version: number;
  redacted: boolean;
}

interface AppErrorShape {
  code: string;
  message: string;
  detail?: Record<string, unknown>;
}

async function demoInvoke(cmd: string, args: Record<string, unknown>): Promise<unknown> {
  return mockInvoke(cmd, args);
}

async function expectAppError(promise: Promise<unknown>, code: string, detail?: Record<string, unknown>): Promise<AppErrorShape> {
  try {
    await promise;
    expect.unreachable(`expected ${code} error`);
  } catch (e) {
    expect(e).toMatchObject({ code });
    const error = e as AppErrorShape;
    if (detail !== undefined) {
      expect(error.detail).toMatchObject(detail);
    }
    return error;
  }
  throw new Error("unreachable");
}

describe("demo memory_* commands", () => {
  it("defaults to opt-in switches off and serves the seeded entry", async () => {
    const settings = (await demoInvoke("memory_settings_get", { scope })) as {
      injectionEnabled: boolean;
      toolsEnabled: boolean;
      version: number;
    };
    expect(settings).toEqual({ injectionEnabled: false, toolsEnabled: false, version: 0 });

    const index = (await demoInvoke("memory_index", { scope })) as { topic: string; entries: { id: string }[] }[];
    expect(index.length).toBeGreaterThan(0);
    expect(index[0].entries.length).toBeGreaterThan(0);
  });

  it("rejects an empty scope everywhere, mirroring the kernel's normalizeScope", async () => {
    const empty = { tenant: "", subject: "" };
    await expectAppError(demoInvoke("memory_create", { scope: empty, topic: "t", content: "c" }), "forbidden");
    await expectAppError(demoInvoke("memory_get", { scope: empty, id: "mem-nginx-restart" }), "forbidden");
    await expectAppError(demoInvoke("memory_index", { scope: empty }), "forbidden");
    await expectAppError(demoInvoke("memory_settings_get", { scope: empty }), "forbidden");
    await expectAppError(demoInvoke("memory_settings_set", { scope: empty, expectedVersion: 0, toolsEnabled: true }), "forbidden");
    // 空 scope 的 forbidden 先于行查找：不存在的 id 也回 forbidden 而不是 not_found。
    await expectAppError(demoInvoke("memory_get", { scope: empty, id: "mem-missing" }), "forbidden");
  });

  it("runs the CRUD round trip with CAS and scope isolation", async () => {
    const created = (await demoInvoke("memory_create", {
      scope,
      topic: "demo-ops",
      content: "restart at 02:00",
    })) as DemoEntry;
    expect(created.version).toBe(1);
    expect(created.redacted).toBe(false);

    const got = (await demoInvoke("memory_get", { scope, id: created.id })) as DemoEntry;
    expect(got.content).toBe("restart at 02:00");

    await expectAppError(demoInvoke("memory_get", { scope: otherScope, id: created.id }), "forbidden");

    const edited = (await demoInvoke("memory_edit", {
      scope,
      id: created.id,
      expectedVersion: 1,
      content: "restart at 03:00",
    })) as DemoEntry;
    expect(edited.version).toBe(2);

    // CAS 冲突必须携带 expected/actual detail，与内核 IPC 合约一致。
    await expectAppError(
      demoInvoke("memory_edit", { scope, id: created.id, expectedVersion: 1, content: "stale" }),
      "bad_param",
      { id: created.id, expected: 1, actual: 2 },
    );
    await expectAppError(demoInvoke("memory_delete", { scope, id: created.id, expectedVersion: 1 }), "bad_param", {
      expected: 1,
      actual: 2,
    });

    await demoInvoke("memory_delete", { scope, id: created.id, expectedVersion: 2 });
    await expectAppError(demoInvoke("memory_get", { scope, id: created.id }), "not_found");
  });

  it("rejects blank content on edit like the kernel's validateContent", async () => {
    const created = (await demoInvoke("memory_create", { scope, topic: "demo-blank", content: "original" })) as DemoEntry;
    await expectAppError(
      demoInvoke("memory_edit", { scope, id: created.id, expectedVersion: 1, content: "   " }),
      "bad_param",
    );
    const untouched = (await demoInvoke("memory_get", { scope, id: created.id })) as DemoEntry;
    expect(untouched.content).toBe("original");
    expect(untouched.version).toBe(1);
  });

  it("rejects every secret shape the kernel rejects, and redacts on request", async () => {
    const secrets: [string, string][] = [
      ["assignment", "password=hunter2"],
      ["private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA7\n-----END RSA PRIVATE KEY-----"],
      ["bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.signature"],
      ["jwt", "token is eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"],
      ["uri credential", "mysql://admin:s3cret@db.internal:3306/app"],
    ];
    for (const [name, content] of secrets) {
      await expectAppError(
        demoInvoke("memory_create", { scope, topic: `demo-sec-${name}`, content }),
        "bad_param",
      );
      const redacted = (await demoInvoke("memory_create", {
        scope,
        topic: `demo-sec-${name}`,
        content,
        secrets: "redact",
      })) as DemoEntry;
      expect(redacted.redacted).toBe(true);
      expect(redacted.content).toContain("[REDACTED]");
      expect(redacted.content).not.toContain("hunter2");
      expect(redacted.content).not.toContain("s3cret");
    }
  });

  it("updates settings with CAS and requires at least one flag", async () => {
    const updated = (await demoInvoke("memory_settings_set", {
      scope,
      expectedVersion: 0,
      injectionEnabled: true,
    })) as { injectionEnabled: boolean; toolsEnabled: boolean; version: number };
    expect(updated).toEqual({ injectionEnabled: true, toolsEnabled: false, version: 1 });

    await expectAppError(
      demoInvoke("memory_settings_set", { scope, expectedVersion: 0, toolsEnabled: true }),
      "bad_param",
      { id: "memory-settings", expected: 0, actual: 1 },
    );
    await expectAppError(demoInvoke("memory_settings_set", { scope, expectedVersion: 1 }), "bad_param");

    const both = (await demoInvoke("memory_settings_set", {
      scope,
      expectedVersion: 1,
      toolsEnabled: true,
    })) as { injectionEnabled: boolean; toolsEnabled: boolean; version: number };
    expect(both).toEqual({ injectionEnabled: true, toolsEnabled: true, version: 2 });
  });
});
