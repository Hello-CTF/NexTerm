/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { devices: vi.fn(), sessions: vi.fn() };
});

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      ...original.authApi,
      devices: mocks.devices,
      sessions: mocks.sessions,
      totpStatus: vi.fn().mockResolvedValue({ enabled: false, recovery_codes_remaining: 0 }),
    },
  };
});

import { AuthCard } from "../features/settings/AuthCard";
import { useAuth } from "../features/auth/store";

const device = (n: number, revoked: boolean) => ({
  id: `d${n}`,
  name: `device-${n}`,
  kind: "agent",
  created_at: 1,
  last_seen_at: 1,
  revoked_at: revoked ? 2 : 0,
});

let mounted: MountedView | undefined;

beforeEach(() => {
  document.body.replaceChildren();
  mocks.sessions.mockResolvedValue({ sessions: [] });
  useAuth.setState({
    status: { initialized: true, auth: "on", registration_open: false },
    user: {
      id: "u1", username: "root", display_name: "", role: "superadmin", state: "active",
      must_change_password: false, mfa_enabled: false, created_at: 1, updated_at: 1, last_login_at: 1,
    },
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(AuthCard)));
  return mounted;
}

describe("registered device list", () => {
  it("hides revoked devices by default and reveals them on request", async () => {
    mocks.devices.mockResolvedValue({ devices: [device(1, false), device(2, true), device(3, true)] });
    const view = mountCard();
    await flush();
    const text = () => view.container.textContent ?? "";
    expect(text()).toContain("device-1");
    expect(text()).not.toContain("device-2");
    expect(text()).toContain("显示已吊销（2）");
    const toggle = Array.from(view.container.querySelectorAll("label")).find((l) => l.textContent?.includes("显示已吊销"))!
      .querySelector("input")!;
    act(() => toggle.click());
    await flush();
    expect(text()).toContain("device-2");
    expect(text()).toContain("device-3");
  });

  it("pages long device lists", async () => {
    mocks.devices.mockResolvedValue({ devices: Array.from({ length: 11 }, (_, i) => device(i + 1, false)) });
    const view = mountCard();
    await flush();
    const text = () => view.container.textContent ?? "";
    expect(text()).toContain("第 1 / 2 页");
    expect(text()).toContain("device-8");
    expect(text()).not.toContain("device-9");
    clickButton(view.container, "下一页");
    await flush();
    expect(text()).toContain("device-9");
    expect(text()).toContain("第 2 / 2 页");
  });
});
