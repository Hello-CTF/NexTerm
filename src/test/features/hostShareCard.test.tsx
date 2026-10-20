/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
    active: null as null | { deviceId: string; onClose: () => void },
    closed: vi.fn(),
  };
});

vi.mock("../../features/sharing/HostShareTerminalApp", async () => {
  const { useEffect } = await import("react");
  return {
    HostShareTerminalApp: (props: { deviceId: string; onClose: () => void }) => {
      mocks.active = props;
      useEffect(() => () => mocks.closed(), []);
      return null;
    },
  };
});

import { ShareCard } from "../../features/settings/ShareCard";
import { useAuth } from "../../features/auth/store";

const NOW = Date.now();
const ROOT = {
  id: "u-root", username: "root", display_name: "Root", role: "superadmin" as const,
  state: "active" as const, must_change_password: false, mfa_enabled: false,
  created_at: 1, updated_at: 1, last_login_at: 1,
};
const ALICE = { ...ROOT, id: "u-alice", username: "alice", role: "user" as const };
const RECEIVED = {
  id: "hs-received", owner_id: "u-owner", owner_username: "owner", device_id: "shared-device",
  recipient_id: "u-root", recipient_username: "root", permission: "read_write",
  created_at: NOW - 1000, expires_at: NOW + 60_000,
};
const GRANTED = { ...RECEIVED, id: "hs-granted", owner_id: "u-root", recipient_id: "u-other", recipient_username: "other" };
const REVOKED = { ...RECEIVED, id: "hs-revoked", revoked_at: NOW - 1000 };
const EXPIRED = { ...RECEIVED, id: "hs-expired", expires_at: NOW - 1000 };
const THIRD = { ...RECEIVED, id: "hs-third", recipient_id: "u-third", recipient_username: "third" };

let mounted: MountedView | undefined;

function seed(user: typeof ROOT | typeof ALICE) {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" }, user,
    dek: null, gate: "ready", pendingRecoveryKey: null, error: null,
  });
}

beforeEach(() => {
  mocks.active = null;
  mocks.fetch.mockImplementation(async (url: string) => {
    const path = String(url);
    const payload = path === "/fleet/devices" ? { devices: [] }
      : path === "/share/host-shares" ? { shares: [RECEIVED, GRANTED, REVOKED, EXPIRED, THIRD] }
        : path === "/share/links" ? { links: [] }
          : { error: { code: "not_found", message: path } };
    return { ok: true, status: 200, text: async () => JSON.stringify(payload) };
  });
  vi.stubGlobal("fetch", mocks.fetch);
  seed(ROOT);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("ShareCard host share recipient entry", () => {
  it("offers open only on the current recipient's effective rows", async () => {
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("来自 owner") ?? false);
    const buttons = [...document.querySelectorAll("button")].filter((button) => button.textContent === "打开终端");
    expect(buttons).toHaveLength(1);
    const row = buttons[0].closest("div.border-b");
    expect(row?.textContent).toContain("来自 owner");
    expect(row?.textContent).not.toContain("已吊销");
    expect(row?.textContent).not.toContain("已过期");
  });

  it("opens the dedicated viewer and closes it on demand", async () => {
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("来自 owner") ?? false);
    const button = [...document.querySelectorAll("button")].find((item) => item.textContent === "打开终端")!;
    await act(async () => {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(mocks.active?.deviceId).toBe("shared-device");
    act(() => mocks.active!.onClose());
    expect(mocks.closed).toHaveBeenCalledTimes(1);
  });

  it("unmounts the old account viewer on account switch", async () => {
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("来自 owner") ?? false);
    const button = [...document.querySelectorAll("button")].find((item) => item.textContent === "打开终端")!;
    await act(async () => {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(mocks.active).not.toBeNull();
    act(() => seed(ALICE));
    await flushUntil(() => {
      const refresh = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
        (item) => item.textContent === "刷新",
      );
      return mocks.closed.mock.calls.length === 1 && refresh?.disabled === false;
    });
    expect(mocks.closed).toHaveBeenCalledTimes(1);
  });
});
