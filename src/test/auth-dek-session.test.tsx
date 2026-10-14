/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    status: vi.fn(),
    me: vi.fn(),
    logout: vi.fn(),
    dekGet: vi.fn(),
  };
});

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: mocks.status,
      me: mocks.me,
      logout: mocks.logout,
      dekGet: mocks.dekGet,
    },
  };
});

vi.mock("../features/auth/crypto", async (importOriginal) => {
  const original = await importOriginal<typeof import("../features/auth/crypto")>();
  return {
    ...original,
    unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(7)),
  };
});

import { useAuth } from "../features/auth/store";
import { bytesToBase64 } from "../ui/base64";

const sessionUser = {
  id: "u1",
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

const DEK_SESSION_KEY = "nexterm.sessionDek.v1.u1";
const savedDek = new Uint8Array(32).fill(3);

beforeEach(() => {
  sessionStorage.clear();
  useAuth.setState({ user: null, dek: null, gate: "loading", status: null, error: null });
  mocks.status.mockResolvedValue({ auth: "on", initialized: true });
  mocks.me.mockResolvedValue({ user: sessionUser, csrf_token: "csrf-1", mfa_required: false });
  mocks.logout.mockResolvedValue(undefined);
  mocks.dekGet.mockResolvedValue({ dek_envelope: "ZW52", kdf_salt: "c2FsdA", kdf_params: "{}" });
});

describe("DEK session persistence", () => {
  it("restores the unlocked DEK on refresh when sessionStorage contains it for the user", async () => {
    sessionStorage.setItem(DEK_SESSION_KEY, bytesToBase64(savedDek));
    await useAuth.getState().refresh();
    const state = useAuth.getState();
    expect(state.gate).toBe("ready");
    expect(state.dek).toEqual(savedDek);
  });

  it("does not restore a DEK from sessionStorage belonging to another user", async () => {
    sessionStorage.setItem("nexterm.sessionDek.v1.u2", bytesToBase64(savedDek));
    await useAuth.getState().refresh();
    expect(useAuth.getState().dek).toBeNull();
  });

  it("writes sessionStorage in unlockDEK and clears it in logout", async () => {
    useAuth.setState({ user: sessionUser, dek: null, gate: "ready" });
    await useAuth.getState().unlockDEK("pw");
    const stored = sessionStorage.getItem(DEK_SESSION_KEY);
    expect(stored).toBe(bytesToBase64(new Uint8Array(32).fill(7)));
    await useAuth.getState().logout();
    expect(sessionStorage.getItem(DEK_SESSION_KEY)).toBeNull();
  });
});
