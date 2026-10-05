import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

interface DemoVaultStatus {
  initialized: boolean;
  mode: "not_init" | "dpapi" | "master";
  unlocked: boolean;
  autoLockMinutes: number;
}

async function demoInvoke(cmd: string, args?: Record<string, unknown>): Promise<unknown> {
  return mockInvoke(cmd, args);
}

async function expectBadParam(promise: Promise<unknown>): Promise<void> {
  try {
    await promise;
    expect.unreachable("expected bad_param error");
  } catch (e) {
    expect(e).toMatchObject({ code: "bad_param" });
  }
}

describe("demo vault_set_autolock", () => {
  it("accepts 0-1440 integers and serves the new value from vault_status", async () => {
    await demoInvoke("vault_set_autolock", { minutes: 5 });
    let status = (await demoInvoke("vault_status")) as DemoVaultStatus;
    expect(status.autoLockMinutes).toBe(5);

    await demoInvoke("vault_set_autolock", { minutes: 1440 });
    status = (await demoInvoke("vault_status")) as DemoVaultStatus;
    expect(status.autoLockMinutes).toBe(1440);

    await demoInvoke("vault_set_autolock", { minutes: 0 });
    status = (await demoInvoke("vault_status")) as DemoVaultStatus;
    expect(status.autoLockMinutes).toBe(0);
  });

  it("rejects out-of-range, non-integer and non-number input with bad_param and keeps the old value", async () => {
    await demoInvoke("vault_set_autolock", { minutes: 7 });
    for (const minutes of [1441, -1, 1.5, "30", null, undefined]) {
      await expectBadParam(demoInvoke("vault_set_autolock", { minutes }));
    }
    await expectBadParam(demoInvoke("vault_set_autolock"));
    const status = (await demoInvoke("vault_status")) as DemoVaultStatus;
    expect(status.autoLockMinutes).toBe(7);
  });

  it("keeps the setting across mode switches like production", async () => {
    await demoInvoke("vault_set_autolock", { minutes: 15 });
    await demoInvoke("vault_init_master");
    let status = (await demoInvoke("vault_status")) as DemoVaultStatus;
    expect(status.mode).toBe("master");
    expect(status.autoLockMinutes).toBe(15);

    await demoInvoke("vault_init_dpapi");
    status = (await demoInvoke("vault_status")) as DemoVaultStatus;
    expect(status.mode).toBe("dpapi");
    expect(status.autoLockMinutes).toBe(15);
  });
});
