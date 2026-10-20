/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    identity: vi.fn(),
    devices: vi.fn(),
    hostShares: vi.fn(),
    links: vi.fn(),
    adminUsers: vi.fn(),
    active: null as null | { deviceId: string; onClose: () => void },
    closed: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, syncApi: { ...actual.syncApi, status: mocks.status } };
});

vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remoteFleetApi: { ...actual.remoteFleetApi, devices: mocks.devices },
    remoteSharingApi: { ...actual.remoteSharingApi, hostShares: mocks.hostShares, links: mocks.links },
    remoteHostShareApi: { ...actual.remoteHostShareApi, identity: mocks.identity },
    remoteAccountApi: { ...actual.remoteAccountApi, adminUsers: mocks.adminUsers },
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
import type { RemoteHostShareIdentity } from "../../ipc/remoteApi";

const NOW = Date.now();
const RECEIVED = {
  id: "hs-a", owner_id: "u-owner", owner_username: "owner-a", device_id: "shared-a",
  recipient_id: "u-root", recipient_username: "root", permission: "read_write",
  created_at: NOW - 1000, expires_at: NOW + 60_000,
};
const RECEIVED_B = { ...RECEIVED, id: "hs-b", owner_username: "owner-b", device_id: "shared-b" };
const IDENTITY_A: RemoteHostShareIdentity = {
  serverUrl: "https://a.example.com", userId: "u-root", generation: "session-a",
};
const IDENTITY_B: RemoteHostShareIdentity = {
  serverUrl: "https://b.example.com", userId: "u-root", generation: "session-b",
};

let mounted: MountedView | undefined;
let client: QueryClient;

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.active = null;
  mocks.status.mockResolvedValue({ configured: true, loggedIn: true, username: "root", userId: "u-root", verifiedAt: 1 });
  mocks.identity.mockResolvedValue(IDENTITY_A);
  mocks.devices.mockResolvedValue({ devices: [] });
  mocks.hostShares.mockResolvedValue({ shares: [RECEIVED] });
  mocks.links.mockResolvedValue({ links: [] });
  mocks.adminUsers.mockRejectedValue(new Error("forbidden"));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  client.clear();
  vi.clearAllMocks();
});

async function openViewer() {
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(ShareCard)));
  await flushUntil(() => [...document.querySelectorAll("button")].some((button) => button.textContent === "打开终端"));
  const open = [...document.querySelectorAll("button")].find((button) => button.textContent === "打开终端")!;
  act(() => open.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  expect(mocks.active?.deviceId).toBe("shared-a");
}

function invalidateIdentity() {
  let invalidation!: Promise<void>;
  act(() => {
    invalidation = client.invalidateQueries({ queryKey: ["sync-link"] });
  });
  return invalidation;
}

describe("ShareCard desktop stable session identity", () => {
  it("keeps the viewer when round-complete returns the same stable identity", async () => {
    await openViewer();
    const next = deferred<RemoteHostShareIdentity>();
    mocks.identity.mockReturnValue(next.promise);
    const invalidation = invalidateIdentity();
    await flush();
    expect(mocks.closed).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("owner-a");

    next.resolve({ ...IDENTITY_A });
    await invalidation;
    await flush();
    expect(mocks.closed).not.toHaveBeenCalled();
    expect(mocks.active?.deviceId).toBe("shared-a");
    expect(document.body.textContent).toContain("owner-a");
  });

  it("keeps the old viewer during refetch and closes only after a real A-to-B identity change", async () => {
    await openViewer();
    const next = deferred<RemoteHostShareIdentity>();
    mocks.identity.mockReturnValue(next.promise);
    mocks.hostShares.mockResolvedValue({ shares: [RECEIVED_B] });
    const invalidation = invalidateIdentity();
    await flush();
    expect(mocks.closed).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("owner-a");

    next.resolve(IDENTITY_B);
    await invalidation;
    await flushUntil(() => mocks.closed.mock.calls.length === 1);
    await flushUntil(() => document.body.textContent?.includes("owner-b") ?? false);
    expect(mocks.hostShares.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(mocks.closed).toHaveBeenCalledTimes(1);
  });

  it("closes when the login session is replaced on the same server", async () => {
    await openViewer();
    mocks.identity.mockResolvedValue({ ...IDENTITY_A, generation: "session-b" });
    const invalidation = invalidateIdentity();
    await invalidation;
    await flushUntil(() => mocks.closed.mock.calls.length === 1);
    expect(mocks.closed).toHaveBeenCalledTimes(1);
  });

  it("keeps the viewer on a transient identity refresh error with an unchanged account", async () => {
    await openViewer();
    mocks.identity.mockRejectedValue({ code: "disconnected", message: "temporary network failure" });
    const invalidation = invalidateIdentity();
    await invalidation;
    await flush();
    expect(mocks.closed).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("owner-a");
  });

  it("closes when the account becomes logged out", async () => {
    await openViewer();
    mocks.status.mockResolvedValue({ configured: true, loggedIn: false, username: "root", userId: "" });
    mocks.identity.mockRejectedValue({ code: "forbidden", message: "logged out" });
    const invalidation = invalidateIdentity();
    await invalidation;
    await flushUntil(() => mocks.closed.mock.calls.length === 1);
    expect(mocks.closed).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("登录服务端账号后可用");
  });
});
