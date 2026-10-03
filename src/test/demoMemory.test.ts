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

async function demoInvoke(cmd: string, args: Record<string, unknown>): Promise<unknown> {
  return mockInvoke(cmd, args);
}

async function expectAppError(promise: Promise<unknown>, code: string): Promise<void> {
  try {
    await promise;
    expect.unreachable(`expected ${code} error`);
  } catch (e) {
    expect(e).toMatchObject({ code });
  }
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

    await expectAppError(
      demoInvoke("memory_edit", { scope, id: created.id, expectedVersion: 1, content: "stale" }),
      "bad_param",
    );

    await expectAppError(demoInvoke("memory_delete", { scope, id: created.id, expectedVersion: 1 }), "bad_param");
    await demoInvoke("memory_delete", { scope, id: created.id, expectedVersion: 2 });
    await expectAppError(demoInvoke("memory_get", { scope, id: created.id }), "not_found");
  });

  it("rejects secrets by default and redacts on request", async () => {
    await expectAppError(
      demoInvoke("memory_create", { scope, topic: "demo-sec", content: "password=hunter2" }),
      "bad_param",
    );
    const redacted = (await demoInvoke("memory_create", {
      scope,
      topic: "demo-sec",
      content: "password=hunter2",
      secrets: "redact",
    })) as DemoEntry;
    expect(redacted.redacted).toBe(true);
    expect(redacted.content).not.toContain("hunter2");
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
