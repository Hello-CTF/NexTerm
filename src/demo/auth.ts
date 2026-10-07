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
  created_at: now() - 30 * 24 * 3600_000,
  updated_at: now() - 30 * 24 * 3600_000,
  last_login_at: now() - 2 * 3600_000,
};

const demoDevices: AccountDevice[] = [
  { id: "d-demo-1", name: "这台浏览器", kind: "web", created_at: now() - 7 * 24 * 3600_000, last_seen_at: now() - 60_000, revoked_at: 0 },
  { id: "d-demo-2", name: "家里的桌面端", kind: "desktop", created_at: now() - 30 * 24 * 3600_000, last_seen_at: now() - 3 * 3600_000, revoked_at: 0 },
  { id: "d-demo-3", name: "旧笔记本", kind: "desktop", created_at: now() - 80 * 24 * 3600_000, last_seen_at: 0, revoked_at: now() - 10 * 24 * 3600_000 },
];

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
  user: AccountUser | null;
  users: AccountUser[];
  devices: AccountDevice[];
  enrollCodes: { code: string; userId: string; expiresAt: number; consumedAt: number | null }[];
  deviceOwners: Record<string, string>;
  nextUserNum: number;
  nextCodeNum: number;
  nextDeviceNum: number;
}

const state: DemoAuthState = {
  initialized: true,
  registrationOpen: false,
  user: demoSuperadmin,
  users: [demoSuperadmin, demoUser],
  devices: demoDevices,
  enrollCodes: [],
  deviceOwners: {
    "d-demo-1": demoSuperadmin.id,
    "d-demo-2": demoSuperadmin.id,
    "d-demo-3": demoSuperadmin.id,
  },
  nextUserNum: 1,
  nextCodeNum: 1,
  nextDeviceNum: 1,
};

function session(user: AccountUser): AccountSession {
  return { user, csrf_token: `demo-csrf-${user.id}` };
}

function fail(message: string): never {
  throw { code: "forbidden", message };
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
    state.user = user;
    user.last_login_at = now();
    return session(user) as T;
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
    return { registration_open: state.registrationOpen, public_base_url: "" } as T;
  }
  if (path === "/admin/settings" && method === "PUT") {
    state.registrationOpen = Boolean(payload.registration_open);
    return { registration_open: state.registrationOpen, public_base_url: "" } as T;
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
  state.user = demoSuperadmin;
  state.users = [demoSuperadmin, demoUser];
  //  enroll 会 push 设备、登录/吊销会改 last_seen/revoked_at;重置必须拿种子的新副本,否则跨重置泄漏。
  state.devices = demoDevices.map((d) => ({ ...d }));
  state.enrollCodes = [];
  state.deviceOwners = {
    "d-demo-1": demoSuperadmin.id,
    "d-demo-2": demoSuperadmin.id,
    "d-demo-3": demoSuperadmin.id,
  };
  state.nextUserNum = 1;
  state.nextCodeNum = 1;
  state.nextDeviceNum = 1;
}
