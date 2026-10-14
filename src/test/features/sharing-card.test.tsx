/** @vitest-environment jsdom */
// SHARE158 interaction tests: regular owner and super admin share the same creation path (recipient_username exact username,
// no /admin/users access), state rendering for host shares and public links, creation (read-only default / explicit read-write /
// bounded TTL / username trim / inline errors for invalid and disabled recipients), revoke confirmation, no creation entry for public links,
// and account-switch isolation (late responses never write into the new account).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, deferred, flush, flushUntil, mountWithClient, setInputValue, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";

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
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const ALICE = { ...SUPERADMIN, id: "u-alice", username: "alice", role: "user" as const };

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

function seedUser(user: typeof SUPERADMIN | typeof ALICE | null, auth: "on" | "off" | "loopback" = "on") {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth },
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

function recipientInput(): HTMLInputElement {
  const input = [...document.querySelectorAll<HTMLInputElement>(".nx-card input:not([type=checkbox])")].find((i) =>
    i.placeholder?.includes("接收者的用户名"),
  );
  if (!input) throw new Error("recipient username input not found");
  return input;
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

describe("ShareCard creation entry and roles", () => {
  it("gives superadmins and regular users a creation entry, lists only enrolled devices, and never calls /admin/users", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    expect(document.body.textContent).toContain("新建主机分享");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);

    const deviceSelect = document.querySelector<HTMLSelectElement>('select[aria-label="分享主机"]');
    expect(deviceSelect).not.toBeNull();
    // Only enrolled devices (device agent installed, terminal enabled, not revoked) can be shared
    expect([...(deviceSelect?.options ?? [])].map((o) => o.textContent)).toEqual(["选择主机", "web-01", "web-02"]);
    expect(recipientInput()).toBeDefined();
    expect(calls.some((c) => c.url === "/admin/users")).toBe(false);
    mounted.unmount();

    // Regular user: the same creation entry (the server filters the device list by role; here only alice's devices are returned)
    const plainCalls = route(shareHandler({ devices: { devices: [DAEMON_DEVICE_2] } }));
    seedUser(ALICE);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-02") ?? false);
    expect(document.body.textContent).toContain("新建主机分享");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    const plainDeviceSelect = document.querySelector<HTMLSelectElement>('select[aria-label="分享主机"]');
    expect([...(plainDeviceSelect?.options ?? [])].map((o) => o.textContent)).toEqual(["选择主机", "web-02"]);
    expect(plainCalls.some((c) => c.url === "/admin/users")).toBe(false);
  });

  it("shows only the guidance card when logged out, with no buttons or share/device requests and the sole login entry in the account card", async () => {
    const calls = route(shareHandler());
    seedUser(null);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("登录后可以把你接入的设备分享给其他注册用户") ?? false);

    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    expect(calls.filter((c) => c.url.startsWith("/share") || c.url.startsWith("/fleet") || c.url.startsWith("/admin"))).toHaveLength(0);
  });
});

describe("ShareCard auth=off/on/loopback", () => {
  it("auth=off reports disabled accounts and unavailable sharing with no buttons, dead login promise, or share/device requests", async () => {
    const calls = route(shareHandler());
    seedUser(null, "off");
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("关闭了账号功能") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("这台服务器关闭了账号功能（--auth=off），终端分享不可用");
    expect(text).not.toContain("登录后可以把你接入的设备分享给其他注册用户");
    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    expect(calls.filter((c) => c.url.startsWith("/share") || c.url.startsWith("/fleet") || c.url.startsWith("/admin"))).toHaveLength(0);
  });

  it("auth=off renders the disabled state without requests even with a stale session, then restores loading and entries after switching to auth=on", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN, "off");
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("终端分享不可用") ?? false);

    expect(document.body.textContent).not.toContain("新建主机分享");
    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    await flush();
    await flush();
    expect(calls.filter((c) => c.url.startsWith("/share") || c.url.startsWith("/fleet") || c.url.startsWith("/admin"))).toHaveLength(0);
    expect(document.body.textContent).toContain("终端分享不可用");

    seedUser(SUPERADMIN, "on");
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "GET /fleet/devices",
      "GET /share/host-shares",
      "GET /share/links",
    ]);
    expect(document.body.textContent).toContain("新建主机分享");
  });

  it("keeps the login guidance unchanged for logged-out auth=on", async () => {
    const calls = route(shareHandler());
    seedUser(null, "on");
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("登录后可以把你接入的设备分享给其他注册用户") ?? false);

    expect(document.body.textContent).not.toContain("终端分享不可用");
    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    expect(calls.filter((c) => c.url.startsWith("/share") || c.url.startsWith("/fleet") || c.url.startsWith("/admin"))).toHaveLength(0);
  });

  it("keeps the login guidance unchanged for logged-out loopback mode", async () => {
    const calls = route(shareHandler());
    seedUser(null, "loopback");
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("登录后可以把你接入的设备分享给其他注册用户") ?? false);

    expect(document.body.textContent).not.toContain("终端分享不可用");
    expect(mounted.container.querySelectorAll("button").length).toBe(0);
    expect(calls.filter((c) => c.url.startsWith("/share") || c.url.startsWith("/fleet") || c.url.startsWith("/admin"))).toHaveLength(0);
  });
});

describe("ShareCard lists and states", () => {
  it("renders device names, direction, permission, state, and expiry for host shares, plus short session ids and recent access for public links", async () => {
    route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("5 条主机分享");
    expect(text).toContain("3 条公开链接");
    expect(text).toContain("分享给 alice");
    expect(text).toContain("来自 alice");
    expect(text).toContain("alice 分享给 carol");
    expect(text).toContain("只读");
    expect(text).toContain("读写");
    expect(text).toContain("有效");
    expect(text).toContain("已吊销");
    expect(text).toContain("已过期");
    // Session id is displayed truncated, the full id goes into title
    const sessionSpan = [...document.querySelectorAll("span[title]")].find((s) =>
      (s.getAttribute("title") || "").includes("01J4Z8Y7K2M8N3P6Q9R1T5V9X2"),
    );
    expect(sessionSpan?.textContent).toContain("01J4Z8Y7…");
    expect(text).toContain("最近访问");
    expect(text).toContain("通过设备代理在你的接入设备上新建终端");
    expect(text).toContain("公开链接用于访问已有的实时会话");
    expect(text.match(/主机密码或私钥/g)).toHaveLength(1);
  });

  it("shows empty states, load errors with retry, and the offline reason when disconnected", async () => {
    route(shareHandler({ shares: { shares: [] }, links: { links: [] } }));
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("还没有主机分享") ?? false);
    expect(document.body.textContent).toContain("还没有公开链接");
    mounted.unmount();

    route(() => ({ status: 500, payload: { error: { code: "internal", message: "database error" } } }));
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享数据加载失败") ?? false);
    expect(document.body.textContent).toContain("database error");
    mounted.unmount();

    mocks.fetch.mockRejectedValue(new Error("connect ECONNREFUSED"));
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("无法连接服务器") ?? false);
    expect(document.body.textContent).toContain("分享数据加载失败");
  });
});

describe("ShareCard host-share creation", () => {
  it("defaults to read-only for 24 hours, trims the username, submits snake_case fields, and refreshes the list", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "  alice  ");
    const ttlSelect = document.querySelector('select[aria-label="分享有效期"]') as HTMLSelectElement;
    expect(ttlSelect.value).toBe(String(24 * 60 * 60_000));
    // All TTL options fall within the server-side 1 minute - 30 day bounds
    for (const option of [...ttlSelect.options]) {
      const ms = Number(option.value);
      expect(ms).toBeGreaterThanOrEqual(60_000);
      expect(ms).toBeLessThanOrEqual(30 * 24 * 60 * 60_000);
    }
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares" && c.method === "POST"));

    const create = calls.find((c) => c.url === "/share/host-shares" && c.method === "POST");
    expect(create?.body).toEqual({ device_id: "d-1", recipient_username: "alice", write: false, ttl_ms: 86_400_000 });
    await flushUntil(() => calls.filter((c) => c.url === "/share/host-shares" && c.method === "GET").length >= 2);
    expect(document.querySelector('select[aria-label="分享主机"]')).toBeNull();
  });

  it("creates a share as a regular user with recipient_username under the same contract as a superadmin", async () => {
    const calls = route(shareHandler({ devices: { devices: [DAEMON_DEVICE_2] } }));
    seedUser(ALICE);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-02") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-2");
    setInputValue(recipientInput(), "root");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares" && c.method === "POST"));

    const create = calls.find((c) => c.url === "/share/host-shares" && c.method === "POST");
    expect(create?.body).toEqual({ device_id: "d-2", recipient_username: "root", write: false, ttl_ms: 86_400_000 });
    expect(calls.some((c) => c.url === "/admin/users")).toBe(false);
  });

  it("submits write=true and a seven-day ttl_ms when read-write access is explicitly selected", async () => {
    const calls = route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "alice");
    setSelectValue(document.querySelector('select[aria-label="分享有效期"]') as HTMLSelectElement, String(7 * 24 * 60 * 60_000));
    const checkbox = [...document.querySelectorAll('input[type="checkbox"]')].find((i) =>
      i.closest("label")?.textContent?.includes("允许读写"),
    ) as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    click(checkbox);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares" && c.method === "POST"));

    const create = calls.find((c) => c.url === "/share/host-shares" && c.method === "POST");
    expect(create?.body).toEqual({ device_id: "d-1", recipient_username: "alice", write: true, ttl_ms: 604_800_000 });
  });

  it("shows inline not_found for an invalid username and forbidden for a disabled recipient", async () => {
    const calls = route(
      shareHandler({ create: { status: 404, payload: { error: { code: "not_found", message: "not found: user" } } } }),
    );
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "no-such-user");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("not found: user") ?? false);
    mounted.unmount();

    route(
      shareHandler({ create: { status: 403, payload: { error: { code: "forbidden", message: "recipient account is unavailable" } } } }),
    );
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "bob");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("recipient account is unavailable") ?? false);
    expect(calls.filter((c) => c.url === "/share/host-shares" && c.method === "POST").length).toBeGreaterThanOrEqual(1);
  });

  it("shows an inline error when the device agent is offline", async () => {
    const calls = route(
      shareHandler({ create: { status: 503, payload: { error: { code: "disconnected", message: "device agent offline" } } } }),
    );
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "alice");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("device agent offline") ?? false);
    expect(calls.some((c) => c.url === "/share/host-shares" && c.method === "POST")).toBe(true);
  });
});

describe("ShareCard revocation", () => {
  it("requires confirmation before revoking a host share, then POSTs and refreshes; revoked rows no longer offer revoke", async () => {
    const calls = route(shareHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享给 alice") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("分享给 alice") && !d.textContent?.includes("已吊销"));
    const revokeButton = [...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销");
    click(revokeButton as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/host-shares/hs-1/revoke"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("alice");
    await flushUntil(() => calls.filter((c) => c.url === "/share/host-shares" && c.method === "GET").length >= 2);

    // hs-4 is revoked: no revoke button in the row anymore
    const revokedRow = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("分享给 alice") && d.textContent?.includes("已吊销"));
    expect(revokedRow).toBeDefined();
    expect([...(revokedRow?.querySelectorAll("button") ?? [])].some((b) => b.textContent?.trim() === "吊销")).toBe(false);
  });

  it("does not send a revoke request when confirmation is canceled", async () => {
    const calls = route(shareHandler());
    mocks.ask.mockResolvedValue(false);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享给 alice") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("分享给 alice") && !d.textContent?.includes("已吊销"));
    click([...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    await flush();

    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
  });

  it("revokes a public link with POST /share/links/{id}/revoke", async () => {
    const calls = route(shareHandler());
    mocks.ask.mockResolvedValue(true);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find(
      (d) => d.textContent?.includes("会话 01J4Z8Y7") && d.textContent?.includes("最近访问"),
    );
    click([...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/links/l-1/revoke"));
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("公开链接");
  });

  it("lets a regular user revoke their own public link without holding the corresponding device", async () => {
    const ownedLink = { ...LINK_ACTIVE, id: "l-alice", owner_id: "u-alice", device_id: "d-1" };
    const calls = route(shareHandler({
      devices: { devices: [DAEMON_DEVICE_2] },
      shares: { shares: [] },
      links: { links: [ownedLink] },
    }));
    mocks.ask.mockResolvedValue(true);
    seedUser(ALICE);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) =>
      d.textContent?.includes("会话 01J4Z8Y7"),
    );
    const revoke = [...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销");
    expect(revoke).toBeDefined();
    click(revoke as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.url === "/share/links/l-alice/revoke"));
  });
});

describe("ShareCard account isolation while confirmation is open", () => {
  it("does not revoke the old host share after switching accounts while its confirmation is open", async () => {
    const calls = route(shareHandler());
    const dialog = deferred<boolean>();
    mocks.ask.mockReturnValue(dialog.promise);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享给 alice") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find((d) => d.textContent?.includes("分享给 alice") && !d.textContent?.includes("已吊销"));
    click([...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);

    // The confirm dialog is still open: switch to a regular user, confirm only afterwards - the old account's revoke must not be fired.
    seedUser(ALICE);
    await flush();
    dialog.resolve(true);
    await flush();
    await flush();

    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
  });

  it("does not revoke the old public link after switching accounts while its confirmation is open", async () => {
    const calls = route(shareHandler());
    const dialog = deferred<boolean>();
    mocks.ask.mockReturnValue(dialog.promise);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard();
    const row = [...(card?.querySelectorAll("div.border-b") ?? [])].find(
      (d) => d.textContent?.includes("会话 01J4Z8Y7") && d.textContent?.includes("最近访问"),
    );
    click([...(row?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);

    seedUser(ALICE);
    await flush();
    dialog.resolve(true);
    await flush();
    await flush();

    expect(calls.some((c) => c.url.includes("/revoke"))).toBe(false);
  });
});

describe("ShareCard public links support listing and revocation only", () => {
  it("has no creation entry, session ID input, or token display", async () => {
    route(shareHandler());
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard();
    expect(card).toBeDefined();
    // The public links area has no creation-type buttons and shows no tokens (the list contract carries no token)
    const buttons = [...(card?.querySelectorAll("button") ?? [])].map((b) => b.textContent ?? "");
    expect(buttons.some((t) => t.includes("创建链接") || t.includes("新建链接") || t.includes("生成链接"))).toBe(false);
    expect(card?.textContent?.toLowerCase()).not.toContain("token");

    // With the creation form open, the only free-text input in the card is the recipient username (no forged session_id entry)
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    const freeText = [...(card?.querySelectorAll<HTMLInputElement>("input:not([type=checkbox])") ?? [])];
    expect(freeText.length).toBe(1);
    expect(freeText[0]?.placeholder).toContain("接收者的用户名");
  });
});

describe("ShareCard account-switch isolation", () => {
  it("clears old shares immediately after user.id changes and never lets late responses from the old account overwrite the new account", async () => {
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
      if (u === "/share/links" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ links: [] }) };
      }
      return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);
    seedUser(SUPERADMIN);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("新建主机分享") !== null);

    // The old account's list is still in flight before the switch: simulate the AuthGate scenario switching to a regular user, the card is not remounted.
    switched = true;
    seedUser(ALICE);
    await flushUntil(() => document.body.textContent?.includes("分享给 carol") ?? false);

    // The old account's late response arrives only now: it must not overwrite the new account's list.
    lateAdminShares.resolve({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ shares: [SHARE_GRANTED] }),
    });
    await flush();
    await flush();
    expect(document.body.textContent).not.toContain("分享给 alice");
    expect(document.body.textContent).toContain("分享给 carol");
  });

  it("discards an old create response after an account switch without blocking creation for the new account", async () => {
    const lateCreate = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    let createCalls = 0;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      const u = String(url);
      const method = init.method ?? "GET";
      if (u === "/fleet/devices" && method === "GET") {
        return { ok: true, status: 200, text: async () => JSON.stringify({ devices: DEVICES }) };
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
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("还没有主机分享") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "alice");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() => createCalls === 1);

    // Switching to a regular user while creation is in flight: the render-time reset has already cleared the form and creating.
    seedUser(ALICE);
    await flush();
    expect(document.body.textContent).not.toContain("创建中");

    // The old account's creation response arrives only now: it must not trigger the old account's refresh or any write.
    lateCreate.resolve({ ok: true, status: 200, text: async () => JSON.stringify({ id: "hs-old" }) });
    await flush();
    await flush();
    expect(document.body.textContent).not.toContain("hs-old");

    // Switching back to super admin and creating again: creating is not stuck by the old finally.
    seedUser(SUPERADMIN);
    await flushUntil(() => document.body.textContent?.includes("新建主机分享") ?? false);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    // Wait for the device list to be ready before selecting, so empty options do not swallow setSelectValue.
    await flushUntil(
      () => document.querySelector('select[aria-label="分享主机"] option[value="d-1"]') !== null,
    );
    setSelectValue(document.querySelector('select[aria-label="分享主机"]') as HTMLSelectElement, "d-1");
    setInputValue(recipientInput(), "alice");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await waitFor(() => {
      expect(createCalls).toBe(2);
    });
  });
});
