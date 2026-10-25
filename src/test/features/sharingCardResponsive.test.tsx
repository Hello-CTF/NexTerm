/** @vitest-environment jsdom */
// M233 narrow-screen structural regression: at 320px real layout the share row content box is only ~176px; expiry / last-access
// timestamps (en-US format can reach ~188px) with shrink-0 overflow on a single line. This test pins the in-row shrink
// contract: the row may wrap, timestamps and direction text may shrink and wrap (full information is not clipped), the revoke
// button and badge stay shrink-0 permanently, and truncated hostnames keep the full value available via title.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

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

const SHARE_GRANTED = {
  id: "hs-1",
  owner_id: "u-root",
  owner_username: "root",
  device_id: "d-1",
  recipient_id: "u-alice",
  recipient_username: "alice",
  permission: "read",
  created_at: NOW - 3600_000,
  expires_at: NOW + 604_800_000,
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

function routeShareData() {
  mocks.fetch.mockImplementation(async (url: string) => {
    const u = String(url);
    if (u === "/fleet/devices") return { ok: true, status: 200, text: async () => JSON.stringify({ devices: [DAEMON_DEVICE] }) };
    if (u === "/share/host-shares") return { ok: true, status: 200, text: async () => JSON.stringify({ shares: [SHARE_GRANTED, SHARE_RECEIVED] }) };
    return { ok: false, status: 404, text: async () => JSON.stringify({ error: { code: "not_found", message: u } }) };
  });
  vi.stubGlobal("fetch", mocks.fetch);
}

let mounted: MountedView | undefined;

function shareRows(): HTMLElement[] {
  const card = [...document.querySelectorAll<HTMLElement>(".nx-card")].find((c) =>
    c.textContent?.includes("主机分享"),
  );
  if (!card) throw new Error("share card not found");
  return [...card.querySelectorAll<HTMLElement>("div.border-b")];
}

function leafSpans(row: HTMLElement): HTMLElement[] {
  return [...row.querySelectorAll<HTMLElement>("span")].filter((s) => s.children.length === 0);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: SUPERADMIN,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
  routeShareData();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("ShareCard narrow-screen row layout (M233)", () => {
  it("wraps share rows, shrinks and wraps timestamps and direction text, and keeps revoke buttons and badges shrink-0", async () => {
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("web-01") ?? false);

    const rows = shareRows();
    expect(rows.length).toBe(2);
    for (const row of rows) {
      expect(row.className).toContain("flex-wrap");
      const spans = leafSpans(row);
      const timeSpans = spans.filter((s) => /有效期至|最近访问/.test(s.textContent || ""));
      expect(timeSpans.length).toBeGreaterThan(0);
      for (const span of timeSpans) {
        expect(span.className).toContain("min-w-0");
        expect(span.className).not.toContain("shrink-0");
        expect(span.className).not.toContain("truncate");
        expect(span.textContent).toMatch(/有效期至|最近访问/);
        expect(span.textContent).toMatch(/\d{1,2}:\d{2}/);
      }
      for (const el of row.querySelectorAll<HTMLElement>("span, button")) {
        if (el.classList.contains("nx-badge") || el.tagName === "BUTTON") {
          expect(el.className).toContain("shrink-0");
        }
      }
    }

    const hostRow = rows.find((r) => (r.textContent || "").includes("分享给"))!;
    const direction = leafSpans(hostRow).find((s) => /分享给|来自/.test(s.textContent || ""))!;
    expect(direction.className).toContain("min-w-0");
    expect(direction.className).toContain("break-words");
    expect(direction.className).not.toContain("shrink-0");

    const nameSpan = leafSpans(hostRow).find((s) => s.textContent === "web-01")!;
    expect(nameSpan.className).toContain("truncate");
    expect(nameSpan.getAttribute("title")).toBe("web-01");

    const rowButtons = rows.flatMap((r) => [...r.querySelectorAll("button")]);
    expect(rowButtons.length).toBe(3);
    expect(rowButtons.filter((button) => button.textContent?.trim() === "打开终端")).toHaveLength(1);
    expect(rowButtons.filter((button) => button.textContent?.trim() === "吊销")).toHaveLength(2);
    for (const button of rowButtons) {
      expect(button.className).toContain("shrink-0");
    }
  });
});
