/** @vitest-environment jsdom */
// SHARE158 交互测试: 角色差异(超管可创建/普通用户只读列表)、主机分享与公开链接
// 的状态渲染、创建(只读默认/显式读写/有界 TTL/排除设备 owner)、吊销确认、
// 公开链接无创建入口, 以及账号切换隔离(晚到响应不写入新账号)。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, deferred, flush, flushUntil, mount, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    ask: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { ShareCard } from "../../features/settings/ShareCard";
import { useAuth } from "../../features/auth/store";

const NOW = Date.now();

const SUPERADMIN = {
  id: "u-root",
  username: "root",
  display_name: "Root",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const ALICE = { ...SUPERADMIN, id: "u-alice", username: "alice", role: "user" as const };
const BOB_DISABLED = { ...SUPERADMIN, id: "u-bob", username: "bob", role: "user" as const, state: "disabled" as const };
const CAROL = { ...SUPERADMIN, id: "u-carol", username: "carol", role: "user" as const };

const AGENT_INFO = {
  platform: "linux",
  app_version: "0.2.2",
  desired_autostart: true,
  terminal_enabled: true,
  current_url: "https://nexterm.example.com",
  service_state: { installed: true, enabled: true, active: true, last_reconcile_at: NOW - 60_000 },
  last_seen_at: NOW - 30_000,
};

const DAEMON_DEVICE = {
  id: "d-1",
  name: "web-01",
  kind: "agent",
  created_at: NOW - 86400_000,
  last_seen_at: NOW - 30_000,
  revoked_at: 0,
  owner: { id: "u-root", username: "root" },
  agent: AGENT_INFO,
};

const DAEMON_DEVICE_2 = {
  ...DAEMON_DEVICE,
  id: "d-2",
  name: "web-02",
  owner: { id: "u-alice", username: "alice" },
};

const TERMINAL_OFF_DEVICE = {
  ...DAEMON_DEVICE,
  id: "d-5",
  name: "db-05",
  agent: { ...AGENT_INFO, terminal_enabled: false },
};

const NO_AGENT_DEVICE = {
  id: "d-3",
  name: "browser-01",
  kind: "browser",
  created_at: NOW - 3600_000,
  last_seen_at: NOW - 60_000,
  revoked_at: 0,
};

const REVOKED_DEVICE = { ...DAEMON_DEVICE, id: "d-4", name: "old-laptop", revoked_at: NOW - 3600_000 };

const DEVICES = [DAEMON_DEVICE, DAEMON_DEVICE_2, TERMINAL_OFF_DEVICE, NO_AGENT_DEVICE, REVOKED_DEVICE];

const SHARE_GRANTED = {
  id: "hs-1",
  owner_id: "u-root",
  owner_username: "root",
  device_id: "d-1",
  recipient_id: "u-alice",
  recipient_username: "alice",
  permission: "read",
  created_at: NOW - 3600_000,
  expires_at: NOW + 3600_000,
};

const SHARE_RECEIVED = {
  id: "hs-2",
  owner_id: "u-alice",
  owner_username: "alice",
  device_id: "d-2",
  recipient_id: "u-root",
  recipient_username: "root",
  permission: "read_write",
  created_at: NOW - 7200_000,
  expires_at: NOW + 7200_000,
};

const SHARE_THIRD = {
  ...SHARE_GRANTED,
  id: "hs-3",
  owner_id: "u-alice",
  owner_username: "alice",
  recipient_id: "u-carol",
  recipient_username: "carol",
};

const SHARE_REVOKED = { ...SHARE_GRANTED, id: "hs-4", revoked_at: NOW - 600_000 };
const SHARE_EXPIRED = { ...SHARE_GRANTED, id: "hs-5", expires_at: NOW - 60_000 };

const LINK_ACTIVE = {
  id: "l-1",
  owner_id: "u-root",
  device_id: "d-1",
  session_id: "01J4Z8Y7K2M8N3P6Q9R1T5V9X2",
  permission: "read",
  created_at: NOW - 1800_000,
  expires_at: NOW + 1800_000,
  last_accessed_at: NOW - 300_000,
};

const LINK_REVOKED = { ...LINK_ACTIVE, id: "l-2", revoked_at: NOW - 600_000, last_accessed_at: undefined };
const LINK_EXPIRED = { ...LINK_ACTIVE, id: "l-3", expires_at: NOW - 60_000, last_accessed_at: undefined };

interface RecordedCall {
  url: string;
  method: string;
  body?: unknown;
}

function route(
  handler: (url: string, method: string, body?: unknown) => { status?: number; payload: unknown },
): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url: u, method, ...(body === undefined ? {} : { body }) });
    const result = handler(u, method, body);
    const status = result.status ?? 200;
    return {
      ok: status < 400,
      status,
      text: async () => JSON.stringify(result.payload),
    };
  });
  vi.stubGlobal("fetch", mocks.fetch);
  return calls;
}

function shareHandler(overrides?: {
  devices?: unknown;
  shares?: unknown;
  links?: unknown;
  create?: { status?: number; payload: unknown };
}) {
  return (url: string, method: string): { status?: number; payload: unknown } => {
    if (url === "/fleet/devices" && method === "GET") {
      return { payload: overrides?.devices ?? { devices: DEVICES } };
    }
    if (url === "/admin/users" && method === "GET") {
      return { payload: { users: [SUPERADMIN, ALICE, BOB_DISABLED, CAROL] } };
    }
    if (url === "/share/host-shares" && method === "GET") {
      return {
        payload:
          overrides?.shares ??
          ({ shares: [SHARE_GRANTED, SHARE_RECEIVED, SHARE_THIRD, SHARE_REVOKED, SHARE_EXPIRED] } as unknown),
      };
    }
    if (url === "/share/links" && method === "GET") {
      return {
        payload: overrides?.links ?? ({ links: [LINK_ACTIVE, LINK_REVOKED, LINK_EXPIRED] } as unknown),
      };
    }
    if (url === "/share/host-shares" && method === "POST") {
      return { status: overrides?.create?.status ?? 200, payload: overrides?.create?.payload ?? { id: "hs-new" } };
    }
    if (/\/revoke$/.test(url) && method === "POST") return { payload: { ok: true } };
    return { status: 404, payload: { error: { code: "not_found", message: url } } };
  };
}

let mounted: MountedView | undefined;

function seedUser(user: typeof SUPERADMIN | typeof ALICE | null) {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

function shareCard(): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>(".nx-card")].find((c) =>
    c.textContent?.includes("主机分享"),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("分享卡片 · 角色差异", () => {
  it("超管看到新建入口, 设备与接收用户下拉来自真实列表且排除设备 owner", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(document.body.textContent).toContain("新建主机分享");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);

    const deviceSelect = document.querySelector<HTMLSelectElement>('select[aria-label="分享主机"]');
    expect(deviceSelect).not.toBeNull();
    // 只有守护主机 (agent 在线且终端开启、未吊销) 可分享
    expect([...(deviceSelect?.options ?? [])].map((o) => o.textContent)).toEqual(["选择主机", "web-01", "web-02"]);

    // d-1 owner 是 root: 选中后 root 从接收用户中排除
    setSelectValue(deviceSelect as HTMLSelectElement, "d-1");
    let recipientSelect = document.querySelector<HTMLSelectElement>('select[aria-label="接收用户"]');
    let recipientNames = [...(recipientSelect?.options ?? [])].map((o) => o.textContent);
    expect(recipientNames).toContain("alice");
    expect(recipientNames).not.toContain("root");
    expect(recipientNames).not.toContain("bob"); // 禁用账号不可接收

    // d-2 owner 是 alice: 选中后 alice 被排除, 之前选中的 root 若与新 owner 冲突会被清掉
    setSelectValue(deviceSelect as HTMLSelectElement, "d-2");
    recipientSelect = document.querySelector<HTMLSelectElement>('select[aria-label="接收用户"]');
    recipientNames = [...(recipientSelect?.options ?? [])].map((o) => o.textContent);
    expect(recipientNames).toContain("root");
    expect(recipientNames).not.toContain("alice");
    expect(recipientNames).toContain("carol");

    expect(calls.some((c) => c.url === "/admin/users")).toBe(true);
  });

  it("普通用户没有新建入口与用户目录请求, 收到与授予的分享都能看到", async () => {
    const calls = route(shareHandler());
    seedUser(ALICE);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(document.querySelector('select[aria-label="分享主机"]')).toBeNull();
    expect(document.body.textContent).toContain("仅超管可读取");
    expect(calls.some((c) => c.url === "/admin/users")).toBe(false);
    // alice 授予给 carol 的分享: owner 是 alice (我), 可吊销
    const card = shareCard();
    expect(card?.textContent).toContain("授予给 carol");
    // alice 接收自 root 的分享也在列表里
    expect(card?.textContent).toContain("接收自 root");
  });

  it("未登录时提示先登录, 不发任何分享/设备请求", async () => {
    const calls = route(shareHandler());
    seedUser(null);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("去登录") ?? false);

    expect(calls.filter((c) => c.url.startsWith("/share") || c.url.startsWith("/fleet") || c.url.startsWith("/admin"))).toHaveLength(0);
  });
});

describe("分享卡片 · 列表与状态", () => {
  it("主机分享渲染设备名/方向/权限/状态/有效期, 公开链接渲染会话短 id 与最近访问", async () => {
    route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("5 条主机分享");
    expect(text).toContain("3 条公开链接");
    expect(text).toContain("授予给 alice");
    expect(text).toContain("接收自 alice");
    expect(text).toContain("alice 授予给 carol");
    expect(text).toContain("只读");
    expect(text).toContain("读写");
    expect(text).toContain("有效");
    expect(text).toContain("已吊销");
    expect(text).toContain("已过期");
    // 会话 id 截断展示, 完整 id 进 title
    const sessionSpan = [...document.querySelectorAll("span[title]")].find((s) =>
      (s.getAttribute("title") || "").includes("01J4Z8Y7K2M8N3P6Q9R1T5V9X2"),
    );
    expect(sessionSpan?.textContent).toContain("01J4Z8Y7…");
    expect(text).toContain("最近访问");
  });

  it("空列表给空态, 加载失败给错误与重试, 断网显示离线原因", async () => {
    route(shareHandler({ shares: { shares: [] }, links: { links: [] } }));
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("还没有主机分享") ?? false);
    expect(document.body.textContent).toContain("还没有公开链接");
    mounted.unmount();

    route(() => ({ status: 500, payload: { error: { code: "internal", message: "数据库错误" } } }));
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享数据加载失败") ?? false);
    expect(document.body.textContent).toContain("数据库错误");
    mounted.unmount();

    mocks.fetch.mockRejectedValue(new Error("connect ECONNREFUSED"));
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("无法连接服务器") ?? false);
    expect(document.body.textContent).toContain("分享数据加载失败");
  });
});

describe("分享卡片 · 创建主机分享", () => {
  it("默认只读 + 24 小时: 提交蛇形请求体并刷新列表", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setSelectValue(document.querySelector('select[aria-label="接收用户"]') as HTMLSelectElement, "u-alice");
    const ttlSelect = document.querySelector('select[aria-label="分享有效期"]') as HTMLSelectElement;
    expect(ttlSelect.value).toBe(String(24 * 60 * 60_000));
    // TTL 选项全部落在服务端 1 分钟-30 天边界内
    for (const option of [...ttlSelect.options]) {
      const ms = Number(option.value);
      expect(ms).toBeGreaterThanOrEqual(60_000);
      expect(ms).toBeLessThanOrEqual(30 * 24 * 60 * 60_000);
    }
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares" && c.method === "POST"));

    const create = calls.find((c) => c.url === "/share/host-shares" && c.method === "POST");
    expect(create?.body).toEqual({ device_id: "d-1", recipient_id: "u-alice", write: false, ttl_ms: 86_400_000 });
    await flushUntil(() => calls.filter((c) => c.url === "/share/host-shares" && c.method === "GET").length >= 2);
    expect(document.querySelector('select[aria-label="分享主机"]')).toBeNull();
  });

  it("显式读写 + 7 天: write 与 ttl_ms 随选择提交", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setSelectValue(document.querySelector('select[aria-label="接收用户"]') as HTMLSelectElement, "u-alice");
    setSelectValue(document.querySelector('select[aria-label="分享有效期"]') as HTMLSelectElement, String(7 * 24 * 60 * 60_000));
    const checkbox = [...document.querySelectorAll('input[type="checkbox"]')].find((i) =>
      i.closest("label")?.textContent?.includes("允许读写"),
    ) as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    click(checkbox);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares" && c.method === "POST"));

    const create = calls.find((c) => c.url === "/share/host-shares" && c.method === "POST");
    expect(create?.body).toEqual({ device_id: "d-1", recipient_id: "u-alice", write: true, ttl_ms: 604_800_000 });
  });

  it("切换主机时清掉与新设备 owner 冲突的接收者", async () => {
    route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    const deviceSelect = document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement;
    const recipientSelect = () => document.querySelector('select[aria-label="接收用户"]') as HTMLSelectElement;
    setSelectValue(deviceSelect, "d-2");
    setSelectValue(recipientSelect(), "u-root");
    expect(recipientSelect().value).toBe("u-root");
    setSelectValue(deviceSelect, "d-1");
    expect(recipientSelect().value).toBe("");
  });

  it("创建失败给内联错误 (如设备代理离线)", async () => {
    const calls = route(
      shareHandler({ create: { status: 503, payload: { error: { code: "disconnected", message: "设备代理离线" } } } }),
    );
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setSelectValue(document.querySelector('select[aria-label="接收用户"]') as HTMLSelectElement, "u-alice");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("设备代理离线") ?? false);
    expect(calls.some((c) => c.url === "/share/host-shares" && c.method === "POST")).toBe(true);
  });
});

describe("分享卡片 · 吊销", () => {
  it("吊销主机分享需确认, 确认后 POST 并刷新; 已吊销行不再给吊销按钮", async () => {
    const calls = route(shareHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("授予给 alice") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("授予给 alice") && !d.textContent?.includes("已吊销"));
    const revokeButton = [...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销");
    click(revokeButton as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares/hs-1/revoke"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("alice");
    await flushUntil(() => calls.filter((c) => c.url === "/share/host-shares" && c.method === "GET").length >= 2);

    // hs-4 已吊销: 行内不再有吊销按钮
    const revokedRow = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("授予给 alice") && d.textContent?.includes("已吊销"));
    expect(revokedRow).toBeDefined();
    expect([...(revokedRow?.querySelectorAll("button") ?? [])].some((b) => b.textContent?.trim() === "吊销")).toBe(false);
  });

  it("吊销确认被取消时不发请求", async () => {
    const calls = route(shareHandler());
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("授予给 alice") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("授予给 alice") && !d.textContent?.includes("已吊销"));
    click([...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    await flush();

    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
  });

  it("吊销公开链接走 POST /share/links/{id}/revoke", async () => {
    const calls = route(shareHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find(
      (d) => d.textContent?.includes("会话 01J4Z8Y7") && d.textContent?.includes("最近访问"),
    );
    click([...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/links/l-1/revoke"));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("公开链接");
  });
});

describe("分享卡片 · 公开链接只有列表与吊销", () => {
  it("没有创建入口/会话 id 输入框/token 展示", async () => {
    route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard();
    expect(card).toBeDefined();
    // 整个卡片没有任何自由文本输入 (无伪造 session_id 入口)
    expect(card?.querySelectorAll("input:not([type=checkbox])")).toHaveLength(0);
    // 公开链接区域没有创建类按钮
    const buttons = [...(card?.querySelectorAll("button") ?? [])].map((b) => b.textContent ?? "");
    expect(buttons.some((t) => t.includes("创建链接") || t.includes("新建链接") || t.includes("生成链接"))).toBe(false);
    // 不展示任何 token (列表合同不含 token)
    expect(card?.textContent?.toLowerCase()).not.toContain("token");
  });
});

describe("分享卡片 · 账号切换隔离", () => {
  it("user.id 变化后旧分享立即清除, 旧账号晚到响应不覆盖新账号", async () => {
    const lateAdminShares = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    let switched = false;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/share/host-shares" && method === "GET") {
        if (!switched) return lateAdminShares.promise;
        return {
          ok: true,
          status: 200,
          text: async () => JSON.stringify({ shares: [{ ...SHARE_GRANTED, id: "hs-9", owner_id: "u-alice", owner_username: "alice", recipient_id: "u-carol", recipient_username: "carol" }] }),
        };
      }
      if (u === "/fleet/devices" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: DEVICES }) };
      }
      if (u === "/admin/users" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ users: [SUPERADMIN, ALICE, CAROL] }) };
      }
      if (u === "/share/links" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ links: [] }) };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("新建主机分享") !== null);

    // 切换前旧账号的列表还在途中: 模拟 AuthGate 场景改登普通用户, 卡片不重挂载。
    switched = true;
    seedUser(ALICE);
    await flushUntil(() => document.body.textContent?.includes("授予给 carol") ?? false);

    // 旧账号的晚到响应此时才到达: 不得覆盖新账号列表。
    lateAdminShares.resolve({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ shares: [SHARE_GRANTED] }),
    });
    await flush();
    await flush();
    expect(document.body.textContent).not.toContain("授予给 alice");
    expect(document.body.textContent).toContain("授予给 carol");
  });

  it("创建在途时切换账号: 旧创建响应不写入, 新账号创建不被卡死", async () => {
    const lateCreate = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    let createCalls = 0;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/fleet/devices" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: DEVICES }) };
      }
      if (u === "/admin/users" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ users: [SUPERADMIN, ALICE, CAROL] }) };
      }
      if (u === "/share/host-shares" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ shares: [] }) };
      }
      if (u === "/share/links" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ links: [] }) };
      }
      if (u === "/share/host-shares" && method === "POST") {
        createCalls += 1;
        if (createCalls === 1) return lateCreate.promise;
        return { ok: true, status: 200, text: async () => JSON.stringify({ id: "hs-new-2" }) };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("还没有主机分享") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setSelectValue(document.querySelector('select[aria-label="接收用户"]') as HTMLSelectElement, "u-alice");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => createCalls === 1);

    // 创建在途时改登普通用户: 渲染期重置已清空表单与 creating。
    seedUser(ALICE);
    await flush();
    expect(document.body.textContent).not.toContain("创建中");

    // 旧账号的创建响应此时才到达: 不得触发旧账号刷新或写入。
    lateCreate.resolve({ ok: true, status: 200, text: async () => JSON.stringify({ id: "hs-old" }) });
    await flush();
    await flush();
    expect(document.body.textContent).not.toContain("hs-old");

    // 切回超管重新创建: creating 未被旧 finally 卡死。
    seedUser(SUPERADMIN);
    await flushUntil(() => document.body.textContent?.includes("新建主机分享") ?? false);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    // 等设备/用户目录就绪后再选择, 避免空选项吞掉 setSelectValue。
    await flushUntil(
      () => document.querySelector('select[aria-label="分享主机"] option[value="d-1"]') !== null
        && document.querySelector('select[aria-label="接收用户"] option[value="u-alice"]') !== null,
    );
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setSelectValue(document.querySelector('select[aria-label="接收用户"]') as HTMLSelectElement, "u-alice");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await waitFor(() => {
      expect(createCalls).toBe(2);
    });
  });
});
