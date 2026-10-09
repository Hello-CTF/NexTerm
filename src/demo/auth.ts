// 演示模式的账号假后端:内存态,行为对齐 /auth/* 与 /admin/* 路由形状。
// 默认以演示超管登录,便于直接体验应用;登录/注册/管理操作同样可交互。

import type {
  AccountDevice,
  AccountSession,
  AccountUser,
  DekEnvelopesView,
} from "../ipc/authApi";

const now = () => Date.now();

const demoSuperadmin: AccountUser = {
  id: "u-demo-admin",
  username: "demo",
  display_name: "演示管理员",
  role: "superadmin",
  state: "active",
  must_change_password: false,
  mfa_enabled: false,
  created_at: now() - 90 * 24 * 3600_000,
  updated_at: now() - 90 * 24 * 3600_000,
  last_login_at: now() - 3600_000,
};

const demoUser: AccountUser = {
  id: "u-demo-alice",
  username: "alice",
  display_name: "Alice",
  role: "user",
  state: "active",
  must_change_password: false,
  mfa_enabled: false,
  created_at: now() - 30 * 24 * 3600_000,
  updated_at: now() - 30 * 24 * 3600_000,
  last_login_at: now() - 2 * 3600_000,
};

// demoDevices 是只读种子模板: 初始状态与 reset 一律持有它的新副本,模板本身永不变动。
const demoDevices: AccountDevice[] = [
  { id: "d-demo-1", name: "这台浏览器", kind: "web", created_at: now() - 7 * 24 * 3600_000, last_seen_at: now() - 60_000, revoked_at: 0 },
  { id: "d-demo-2", name: "家里的桌面端", kind: "desktop", created_at: now() - 30 * 24 * 3600_000, last_seen_at: now() - 3 * 3600_000, revoked_at: 0 },
  { id: "d-demo-3", name: "旧笔记本", kind: "desktop", created_at: now() - 80 * 24 * 3600_000, last_seen_at: 0, revoked_at: now() - 10 * 24 * 3600_000 },
];

function seedDevices(): AccountDevice[] {
  return demoDevices.map((d) => ({ ...d }));
}

// 演示信封:形状合法的假 base64,长度为 60/16/60。
const demoEnvelope: DekEnvelopesView = {
  dek_envelope: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  kdf_salt: "AAAAAAAAAAAAAAAAAAAAAA==",
  kdf_params: '{"t":3,"m":65536,"p":4}',
  recovery_envelope: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  recovery_hash: "0000000000000000000000000000000000000000000000000000000000000000",
};

interface DemoAuthState {
  initialized: boolean;
  registrationOpen: boolean;
  mfaRequired: boolean;
  user: AccountUser | null;
  users: AccountUser[];
  devices: AccountDevice[];
  enrollCodes: { code: string; userId: string; expiresAt: number; consumedAt: number | null }[];
  deviceOwners: Record<string, string>;
  totpPending: Record<string, string>;
  totpBindings: Record<string, { recovery: string[] }>;
  mfaTickets: Record<string, { userId: string; expiresAt: number }>;
  nextUserNum: number;
  nextCodeNum: number;
  nextDeviceNum: number;
}

const state: DemoAuthState = {
  initialized: true,
  registrationOpen: false,
  mfaRequired: false,
  user: demoSuperadmin,
  users: [demoSuperadmin, demoUser],
  devices: seedDevices(),
  enrollCodes: [],
  deviceOwners: {
    "d-demo-1": demoSuperadmin.id,
    "d-demo-2": demoSuperadmin.id,
    "d-demo-3": demoSuperadmin.id,
  },
  totpPending: {},
  totpBindings: {},
  mfaTickets: {},
  nextUserNum: 1,
  nextCodeNum: 1,
  nextDeviceNum: 1,
};

function session(user: AccountUser): AccountSession {
  return { user, csrf_token: `demo-csrf-${user.id}`, mfa_required: state.mfaRequired };
}

function fail(message: string): never {
  throw { code: "forbidden", message };
}

const demoBase32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

function demoSecret(): string {
  let out = "";
  for (let i = 0; i < 32; i++) out += demoBase32[Math.floor(Math.random() * demoBase32.length)];
  return out;
}

function demoRecoveryCodes(): string[] {
  const codes: string[] = [];
  for (let i = 0; i < 8; i++) {
    let raw = "";
    for (let j = 0; j < 16; j++) raw += demoBase32[Math.floor(Math.random() * demoBase32.length)];
    codes.push(raw.replace(/(.{4})/g, "$1-").replace(/-$/, ""));
  }
  return codes;
}

function findUser(username: string): AccountUser | undefined {
  return state.users.find((u) => u.username.toLowerCase() === username.toLowerCase());
}

export async function demoAuthRequest<T>(method: string, path: string, body?: unknown): Promise<T> {
  await new Promise((r) => window.setTimeout(r, 60));
  const payload = (body ?? {}) as Record<string, unknown>;

  if (path === "/auth/status" && method === "GET") {
    return { initialized: state.initialized, registration_open: state.registrationOpen, auth: "on" } as T;
  }
  // 与真实后端一致: mfa_required 策略下未绑定会话被锁到只能完成 TOTP 绑定(公共路由不受影响)。
  const mfaEnrollExempt = new Set([
    "/auth/login",
    "/auth/totp/login",
    "/auth/init",
    "/auth/register",
    "/auth/recovery/reset",
    "/auth/devices/enroll",
    "/auth/me",
    "/auth/totp",
    "/auth/totp/setup",
    "/auth/totp/confirm",
    "/auth/logout",
    "/auth/logout-all",
  ]);
  if (state.mfaRequired && state.user && !state.user.mfa_enabled && !mfaEnrollExempt.has(path)) {
    throw { code: "mfa_enrollment_required", message: "管理员已要求启用两步验证: 完成 TOTP 绑定前,该账号只能使用绑定相关功能" };
  }
  if (path === "/auth/me" && method === "GET") {
    if (!state.user) fail("会话无效或缺失");
    return session(state.user) as T;
  }
  if (path === "/auth/login" && method === "POST") {
    const user = findUser(String(payload.username ?? ""));
    if (!user) fail("用户名或密码错误");
    if (user.state === "disabled") fail("账号已禁用");
    const deviceId = String(payload.device_id ?? "");
    if (deviceId) {
      const device = state.devices.find((d) => d.id === deviceId);
      if (!device) throw { code: "not_found", message: "未找到: 设备" };
      if (state.deviceOwners[deviceId] !== user.id) fail("设备不属于该用户");
      if (device.revoked_at) fail("设备已吊销");
      device.last_seen_at = now();
    }
    if (state.totpBindings[user.id]) {
      // 与真实后端一致: 密码通过不直接建会话, 发 MFA 票据等第二因子。
      const ticket = `demo-mfa-${now().toString(36)}-${state.nextCodeNum++}`;
      const expiresAt = now() + 5 * 60_000;
      state.mfaTickets[ticket] = { userId: user.id, expiresAt };
      return { mfa_required: true, ticket, expires_at: expiresAt } as T;
    }
    state.user = user;
    user.last_login_at = now();
    return session(user) as T;
  }
  if (path === "/auth/totp/login" && method === "POST") {
    const record = state.mfaTickets[String(payload.ticket ?? "")];
    if (!record || record.expiresAt <= now()) fail("登录状态已过期，请重新登录");
    const user = state.users.find((u) => u.id === record.userId);
    const binding = user ? state.totpBindings[user.id] : undefined;
    if (!user || !binding) fail("登录状态已过期，请重新登录");
    const code = String(payload.code ?? "").trim();
    const recoveryIndex = binding.recovery.indexOf(code);
    // 演示模式不实现真实 TOTP 算法: 任意 6 位数字或一枚未使用的恢复码即通过。
    if (!/^\d{6}$/.test(code) && recoveryIndex < 0) fail("验证码错误");
    if (recoveryIndex >= 0) binding.recovery.splice(recoveryIndex, 1);
    delete state.mfaTickets[String(payload.ticket ?? "")];
    state.user = user;
    user.last_login_at = now();
    return session(user) as T;
  }
  if (path === "/auth/totp" && method === "GET") {
    if (!state.user) fail("会话无效或缺失");
    const binding = state.totpBindings[state.user.id];
    return {
      enabled: Boolean(binding),
      pending: Boolean(state.totpPending[state.user.id]),
      mfa_required: state.mfaRequired,
      recovery_codes_left: binding?.recovery.length ?? 0,
    } as T;
  }
  if (path === "/auth/totp/setup" && method === "POST") {
    if (!state.user) fail("会话无效或缺失");
    const binding = state.totpBindings[state.user.id];
    if (binding) {
      // 与真实后端一致: 已绑定用户换绑前必须用当前动态码或恢复码重验(演示模式不验密码)。
      const reverify = String(payload.reverify ?? "").trim();
      const recoveryIndex = binding.recovery.indexOf(reverify);
      if (!/^\d{6}$/.test(reverify) && recoveryIndex < 0) fail("已开启两步验证: 换绑前需要当前动态码、恢复码或登录密码");
      if (recoveryIndex >= 0) binding.recovery.splice(recoveryIndex, 1);
    }
    const secret = demoSecret();
    state.totpPending[state.user.id] = secret;
    return { secret, otpauth_uri: `otpauth://totp/NexTerm:${state.user.username}?secret=${secret}&issuer=NexTerm` } as T;
  }
  if (path === "/auth/totp/confirm" && method === "POST") {
    if (!state.user) fail("会话无效或缺失");
    if (!state.totpPending[state.user.id]) throw { code: "bad_param", message: "参数错误: 尚未开始 TOTP 绑定" };
    if (!/^\d{6}$/.test(String(payload.code ?? ""))) fail("验证码错误");
    delete state.totpPending[state.user.id];
    const recovery = demoRecoveryCodes();
    state.totpBindings[state.user.id] = { recovery };
    state.user.mfa_enabled = true;
    return { recovery_codes: [...recovery] } as T;
  }
  if (path === "/auth/totp" && method === "DELETE") {
    if (!state.user) fail("会话无效或缺失");
    const binding = state.totpBindings[state.user.id];
    if (!binding) fail("验证码错误");
    const code = String(payload.code ?? "").trim();
    if (!/^\d{6}$/.test(code) && !binding.recovery.includes(code)) fail("验证码错误");
    delete state.totpBindings[state.user.id];
    delete state.totpPending[state.user.id];
    state.user.mfa_enabled = false;
    return { ok: true } as T;
  }
  if (path === "/auth/init" && method === "POST") {
    if (state.initialized) fail("服务器已完成初始化，不能重复初始化");
    state.initialized = true;
    state.user = demoSuperadmin;
    return session(demoSuperadmin) as T;
  }
  if (path === "/auth/register" && method === "POST") {
    if (!state.registrationOpen) fail("注册已关闭，请联系管理员添加账号");
    const username = String(payload.username ?? "").trim();
    if (!username) fail("参数错误: 用户名不能为空");
    if (findUser(username)) fail("参数错误: 用户名已存在");
    const user: AccountUser = {
      id: `u-demo-${state.nextUserNum++}`,
      username,
      display_name: String(payload.display_name ?? ""),
      role: "user",
      state: "active",
      must_change_password: false,
      mfa_enabled: false,
      created_at: now(),
      updated_at: now(),
      last_login_at: now(),
    };
    state.users.push(user);
    state.user = user;
    return session(user) as T;
  }
  if (path === "/auth/logout" && method === "POST") {
    state.user = null;
    return { ok: true } as T;
  }
  if (path === "/auth/logout-all" && method === "POST") {
    state.user = null;
    return { revoked: 1 } as T;
  }
  if (path === "/auth/password" && method === "POST") {
    if (!state.user) fail("会话无效或缺失");
    return { ok: true } as T;
  }
  if (path === "/auth/recovery/reset" && method === "POST") {
    const user = findUser(String(payload.username ?? ""));
    if (!user) fail("用户名或恢复密钥错误");
    if (user.state === "disabled") fail("账号已禁用");
    state.user = user;
    return { ok: true } as T;
  }
  if (path === "/auth/dek" && method === "GET") {
    if (!state.user) fail("会话无效或缺失");
    return demoEnvelope as T;
  }
  if (path === "/auth/dek" && method === "POST") {
    if (!state.user) fail("会话无效或缺失");
    return { ok: true } as T;
  }
  if (path === "/auth/devices" && method === "GET") {
    if (!state.user) fail("会话无效或缺失");
    // 与真实后端一致每次返回新负载: 直接给活引用会让前端 setState 同引用跳过重渲染,吊销后列表不刷新。
    return { devices: state.devices.map((d) => ({ ...d })) } as T;
  }
  if (path === "/auth/devices/enroll-code" && method === "POST") {
    if (!state.user) fail("会话无效或缺失");
    const code = `demo-enroll-${now().toString(36)}-${state.nextCodeNum++}`;
    const expiresAt = now() + 15 * 60_000;
    state.enrollCodes.push({ code, userId: state.user.id, expiresAt, consumedAt: null });
    return { code, expires_at: expiresAt } as T;
  }
  if (path.startsWith("/auth/devices/") && method === "DELETE") {
    if (!state.user) fail("会话无效或缺失");
    const id = decodeURIComponent(path.slice("/auth/devices/".length));
    const device = state.devices.find((d) => d.id === id);
    if (!device) fail("未找到: 设备");
    device.revoked_at = now();
    return { ok: true } as T;
  }
  if (path === "/auth/devices/enroll" && method === "POST") {
    const code = String(payload.code ?? "");
    const record = state.enrollCodes.find((c) => c.code === code);
    if (!record || record.consumedAt !== null || record.expiresAt <= now()) {
      fail("设备注册码无效或已过期");
    }
    record.consumedAt = now();
    const device: AccountDevice = {
      id: `d-demo-enroll-${state.nextDeviceNum++}`,
      name: String(payload.name ?? "新设备"),
      kind: String(payload.kind ?? "desktop"),
      created_at: now(),
      last_seen_at: 0,
      revoked_at: 0,
    };
    state.devices.push(device);
    state.deviceOwners[device.id] = record.userId;
    return { device } as T;
  }

  if (path === "/admin/users" && method === "GET") {
    return { users: state.users } as T;
  }
  if (path === "/admin/users" && method === "POST") {
    const username = String(payload.username ?? "").trim();
    if (!username) fail("参数错误: 用户名不能为空");
    if (findUser(username)) fail("参数错误: 用户名已存在");
    const user: AccountUser = {
      id: `u-demo-${state.nextUserNum++}`,
      username,
      display_name: String(payload.display_name ?? ""),
      role: "user",
      state: "active",
      must_change_password: true,
      mfa_enabled: false,
      created_at: now(),
      updated_at: now(),
      last_login_at: 0,
    };
    state.users.push(user);
    return { user } as T;
  }
  if (path.startsWith("/admin/users/") && method === "POST") {
    const rest = path.slice("/admin/users/".length);
    const [id, action] = rest.split("/");
    const user = state.users.find((u) => u.id === id);
    if (!user) fail("未找到: 用户");
    if (action === "disable") {
      if (user.id === state.user?.id) fail("不能禁用当前登录账号");
      user.state = "disabled";
      return { ok: true } as T;
    }
    if (action === "reset") {
      user.state = "reset_required";
      user.must_change_password = true;
      return { ok: true } as T;
    }
  }
  if (path === "/admin/settings" && method === "GET") {
    return { registration_open: state.registrationOpen, public_base_url: "", mfa_required: state.mfaRequired } as T;
  }
  if (path === "/admin/settings" && method === "PUT") {
    state.registrationOpen = Boolean(payload.registration_open);
    if (payload.mfa_required !== undefined) state.mfaRequired = Boolean(payload.mfa_required);
    return { registration_open: state.registrationOpen, public_base_url: "", mfa_required: state.mfaRequired } as T;
  }

  if (path === "/sync/v2/ids" && method === "POST") {
    return { protocol: 2, entries: [], head: "demo-head", max_seq: 0 } as T;
  }
  if (path === "/sync/v2/pull" && method === "POST") {
    return { protocol: 2, objects: [], head: "demo-head", max_seq: 0, next_seq: 0, cursor_done: true } as T;
  }
  if (path === "/sync/v2/push" && method === "POST") {
    const objects = (payload.objects as unknown[] | undefined) ?? [];
    return { protocol: 2, head: "demo-head", max_seq: objects.length, applied: objects.length, skipped: 0 } as T;
  }

  throw { code: "not_found", message: `演示路由不存在: ${method} ${path}` };
}

export function resetDemoAuth(): void {
  state.initialized = true;
  state.registrationOpen = false;
  state.mfaRequired = false;
  state.user = demoSuperadmin;
  state.users = [demoSuperadmin, demoUser];
  state.devices = seedDevices();
  state.enrollCodes = [];
  state.deviceOwners = {
    "d-demo-1": demoSuperadmin.id,
    "d-demo-2": demoSuperadmin.id,
    "d-demo-3": demoSuperadmin.id,
  };
  state.totpPending = {};
  state.totpBindings = {};
  state.mfaTickets = {};
  demoSuperadmin.mfa_enabled = false;
  demoUser.mfa_enabled = false;
  // id/配对码计数器保持单调不重置: reset 只恢复数据,跨 reset 不复用任何 id 或码。
}
