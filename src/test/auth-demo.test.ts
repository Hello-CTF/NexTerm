/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it } from "vitest";
import { demoAuthRequest, resetDemoAuth } from "../demo/auth";
import type { AccountDevice } from "../ipc/authApi";

// M197 R1 回归: demo 登记设备的 id 必须与种子设备(d-demo-1..3)绝不冲突,
// 且 login/revoke 只能作用于 enroll 实际返回的设备,不能错绑到同 id 的种子设备。

const SEEDED_IDS = ["d-demo-1", "d-demo-2", "d-demo-3"];

async function issueCode(): Promise<string> {
  const r = await demoAuthRequest<{ code: string; expires_at: number }>("POST", "/auth/devices/enroll-code", {});
  return r.code;
}

async function enroll(code: string, name: string): Promise<AccountDevice> {
  const r = await demoAuthRequest<{ device: AccountDevice }>("POST", "/auth/devices/enroll", { code, name, kind: "web" });
  return r.device;
}

async function listDevices(): Promise<AccountDevice[]> {
  const r = await demoAuthRequest<{ devices: AccountDevice[] }>("GET", "/auth/devices");
  return r.devices;
}

async function login(username: string, deviceId: string): Promise<unknown> {
  return demoAuthRequest("POST", "/auth/login", { username, password: "any", device_id: deviceId });
}

beforeEach(() => {
  resetDemoAuth();
});

describe("demo 配对码登记设备", () => {
  it("enroll 返回的设备 id 不与种子冲突,连续登记互不相同", async () => {
    const d1 = await enroll(await issueCode(), "浏览器 A");
    const d2 = await enroll(await issueCode(), "浏览器 B");
    expect(SEEDED_IDS).not.toContain(d1.id);
    expect(SEEDED_IDS).not.toContain(d2.id);
    expect(d2.id).not.toBe(d1.id);

    const devices = await listDevices();
    expect(devices.filter((d) => d.id === d1.id)).toHaveLength(1);
    expect(devices.filter((d) => d.id === d2.id)).toHaveLength(1);
    const ids = devices.map((d) => d.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("login 更新的是 enroll 返回的设备,种子设备活动时间不受影响", async () => {
    const before = await listDevices();
    const seededBefore = new Map(before.map((d) => [d.id, d.last_seen_at]));

    const enrolled = await enroll(await issueCode(), "新浏览器");
    expect(enrolled.last_seen_at).toBe(0);
    await login("demo", enrolled.id);

    const after = await listDevices();
    const touched = after.find((d) => d.id === enrolled.id);
    expect(touched?.last_seen_at).toBeGreaterThan(0);
    for (const seeded of SEEDED_IDS) {
      expect(after.find((d) => d.id === seeded)?.last_seen_at).toBe(seededBefore.get(seeded));
    }
  });

  it("吊销只作用于 enroll 返回的设备;被吊销后该设备不能再登录", async () => {
    const enrolled = await enroll(await issueCode(), "待吊销浏览器");
    await demoAuthRequest("DELETE", `/auth/devices/${encodeURIComponent(enrolled.id)}`);

    const after = await listDevices();
    expect(after.find((d) => d.id === enrolled.id)?.revoked_at).toBeGreaterThan(0);
    // 种子设备保持各自原状: d-demo-3 原本已吊销, d-demo-1/2 未吊销
    expect(after.find((d) => d.id === "d-demo-1")?.revoked_at).toBe(0);
    expect(after.find((d) => d.id === "d-demo-2")?.revoked_at).toBe(0);
    expect(after.find((d) => d.id === "d-demo-3")?.revoked_at).toBeGreaterThan(0);

    await expect(login("demo", enrolled.id)).rejects.toMatchObject({ code: "forbidden", message: "设备已吊销" });
  });

  it("连续签发两张配对码后,两张都能完成 enroll + login", async () => {
    const codeA = await issueCode();
    const codeB = await issueCode();
    const dA = await enroll(codeA, "设备 A");
    const dB = await enroll(codeB, "设备 B");
    expect(dA.id).not.toBe(dB.id);
    await expect(login("demo", dA.id)).resolves.toMatchObject({ user: { id: "u-demo-admin" } });
    await expect(login("demo", dB.id)).resolves.toMatchObject({ user: { id: "u-demo-admin" } });
  });

  it("未知码与已消费码都按 无效或已过期 拒绝", async () => {
    await expect(enroll("demo-enroll-nonexistent", "x")).rejects.toMatchObject({
      code: "forbidden",
      message: "设备注册码无效或已过期",
    });
    const code = await issueCode();
    await enroll(code, "一次性设备");
    await expect(enroll(code, "重放")).rejects.toMatchObject({
      code: "forbidden",
      message: "设备注册码无效或已过期",
    });
  });
});
