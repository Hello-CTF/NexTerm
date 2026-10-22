/** @vitest-environment jsdom */
// Desktop share card - logged in: host share create/revoke and public link list/revoke all go through the remote* proxies
// (remote_share_* / remote_fleet_devices), the view is isomorphic with WEB; the role is probed via adminUsers.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mountWithClient, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    linkGet: vi.fn(),
    identity: vi.fn(),
    devices: vi.fn(),
    hostShares: vi.fn(),
    createHostShare: vi.fn(),
    revokeHostShare: vi.fn(),
    links: vi.fn(),
    revokeLink: vi.fn(),
    adminUsers: vi.fn(),
    ask: vi.fn(),
    fetch: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: { ...actual.syncApi, status: mocks.status, linkGet: mocks.linkGet },
  };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remoteFleetApi: { ...actual.remoteFleetApi, devices: mocks.devices },
    remoteSharingApi: {
      ...actual.remoteSharingApi,
      hostShares: mocks.hostShares,
      createHostShare: mocks.createHostShare,
      revokeHostShare: mocks.revokeHostShare,
      links: mocks.links,
      revokeLink: mocks.revokeLink,
    },
    remoteHostShareApi: { ...actual.remoteHostShareApi, identity: mocks.identity },
    remoteAccountApi: { ...actual.remoteAccountApi, adminUsers: mocks.adminUsers },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { ShareCard } from "../../features/settings/ShareCard";

const NOW = Date.now();

const SUPERADMIN_USER = {
  id: "u-root",
  username: "root",
  display_name: "Root",
  role: "superadmin",
  state: "active",
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const AGENT_INFO = {
  platform: "linux",
  app_version: "1.2.3",
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

const TERMINAL_OFF_DEVICE = { ...DAEMON_DEVICE, id: "d-2", name: "db-02", agent: { ...AGENT_INFO, terminal_enabled: false } };

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
  ...SHARE_GRANTED,
  id: "hs-2",
  owner_id: "u-alice",
  owner_username: "alice",
  recipient_id: "u-root",
  recipient_username: "root",
  permission: "read_write",
};

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

let mounted: MountedView | undefined;

function seedLoggedIn(admin = true, userId = "u-root") {
  mocks.status.mockResolvedValue({
    configured: true,
    loggedIn: true,
    username: "root",
    userId,
    seq: 0,
    verifiedAt: 1,
    lastError: "",
  });
  mocks.linkGet.mockResolvedValue({ url: "https://a.example.com/", username: "root", verifiedAt: 1 });
  mocks.identity.mockResolvedValue({ serverUrl: "https://a.example.com", userId, generation: "session-a" });
  if (admin) {
    mocks.adminUsers.mockResolvedValue({ users: [SUPERADMIN_USER] });
  } else {
    mocks.adminUsers.mockRejectedValue(new Error("forbidden"));
  }
}

function seedSharing() {
  mocks.devices.mockResolvedValue({ devices: [DAEMON_DEVICE, TERMINAL_OFF_DEVICE] });
  mocks.hostShares.mockResolvedValue({ shares: [SHARE_GRANTED, SHARE_RECEIVED] });
  mocks.links.mockResolvedValue({ links: [LINK_ACTIVE] });
  mocks.createHostShare.mockResolvedValue({ id: "hs-new" });
  mocks.revokeHostShare.mockResolvedValue({ ok: true });
  mocks.revokeLink.mockResolvedValue({ ok: true });
}

function shareCard(): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>(".nx-card")].find((c) => c.textContent?.includes("主机分享"));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  vi.stubGlobal("fetch", mocks.fetch);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("Share card - desktop logged in", () => {
  it("renders device names, directions, permissions, states, and public links through remote proxies without fetch", async () => {
    seedLoggedIn();
    seedSharing();
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("2 条主机分享");
    expect(text).toContain("1 条旧版链接");
    expect(text).toContain("旧版链接（只可吊销）");
    expect(text).toContain("分享给 alice");
    expect(text).toContain("来自 alice");
    expect(text).toContain("web-01");
    expect(mocks.devices).toHaveBeenCalledTimes(1);
    expect(mocks.hostShares).toHaveBeenCalledTimes(1);
    expect(mocks.links).toHaveBeenCalledTimes(1);
    expect(mocks.fetch).not.toHaveBeenCalled();
  });

  it("creates a host share through remote create with a trimmed username and refreshed list, listing only terminal-enabled devices", async () => {
    seedLoggedIn();
    seedSharing();
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建主机分享")) as HTMLButtonElement);
    const deviceSelect = document.querySelector<HTMLSelectElement>('select[aria-label="分享主机"]') as HTMLSelectElement;
    expect([...deviceSelect.options].map((o) => o.textContent)).toEqual(["选择主机", "web-01"]);
    setSelectValue(deviceSelect, "d-1");
    const recipient = [...document.querySelectorAll<HTMLInputElement>("input:not([type=checkbox])")].find((i) =>
      i.placeholder?.includes("接收者的用户名"),
    ) as HTMLInputElement;
    setInputValue(recipient, "  alice  ");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "创建分享") as HTMLButtonElement);
    await flushUntil(() =>
      mocks.createHostShare.mock.calls.length === 1 &&
      mocks.devices.mock.calls.length >= 2 && mocks.hostShares.mock.calls.length >= 2 && mocks.links.mock.calls.length >= 2 &&
      [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "刷新")?.disabled === false,
    );
    expect(mocks.createHostShare).toHaveBeenCalledWith({
      deviceId: "d-1",
      recipientUsername: "alice",
      write: false,
      ttlMs: 24 * 60 * 60_000,
    });
  });

  it("confirms before revoking a host share, then revokes through remote and refreshes", async () => {
    seedLoggedIn();
    seedSharing();
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享给 alice") ?? false);

    const card = shareCard() as HTMLElement;
    const row = [...card.querySelectorAll("div.border-b")].find((d) => d.textContent?.includes("分享给 alice")) as HTMLElement;
    click([...row.querySelectorAll("button")].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.revokeHostShare.mock.calls.length === 1);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.revokeHostShare).toHaveBeenCalledWith("hs-1");
    await flushUntil(() =>
      mocks.devices.mock.calls.length >= 2 && mocks.hostShares.mock.calls.length >= 2 && mocks.links.mock.calls.length >= 2 &&
      [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "刷新")?.disabled === false,
    );
  });

  it("revokes a public link through remote revokeLink", async () => {
    seedLoggedIn();
    seedSharing();
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("01J4Z8Y7") ?? false);

    const card = shareCard() as HTMLElement;
    const row = [...card.querySelectorAll("div.border-b")].find((d) => d.textContent?.includes("会话 01J4Z8Y7")) as HTMLElement;
    click([...row.querySelectorAll("button")].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.revokeLink.mock.calls.length === 1);

    expect(mocks.revokeLink).toHaveBeenCalledWith("l-1");
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("公开链接");
  });

  it("lets a regular user revoke their own granted shares when adminUsers probing fails, with direction copy from their perspective", async () => {
    seedLoggedIn(false);
    seedSharing();
    mocks.ask.mockResolvedValue(true);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("分享给 alice") ?? false);

    const card = shareCard() as HTMLElement;
    // hs-1 granted by me + hs-2 received by me: both can be revoked (owner_id === userId or device holder)
    const grantedRow = [...card.querySelectorAll("div.border-b")].find((d) => d.textContent?.includes("分享给 alice")) as HTMLElement;
    click([...grantedRow.querySelectorAll("button")].find((b) => b.textContent?.trim() === "吊销") as HTMLButtonElement);
    await flushUntil(() => mocks.revokeHostShare.mock.calls.length === 1);
    expect(mocks.revokeHostShare).toHaveBeenCalledWith("hs-1");
  });
});
