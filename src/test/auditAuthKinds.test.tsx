/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flush, flushUntil as flushUntilBase, mount, type MountedView } from "./features/reactTestUtils";
import { AuditView } from "../features/settings/AuditView";
import { useAuth } from "../features/auth/store";

const mocks = vi.hoisted(() => ({ auditQuery: vi.fn(), auditCount: vi.fn() }));

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return { ...actual, assetApi: { ...actual.assetApi, auditQuery: mocks.auditQuery, auditCount: mocks.auditCount } };
});

// Settles the responses that land after the awaited condition, so no state update escapes act.
async function flushUntil(predicate: () => boolean): Promise<void> {
  await flush();
  await flushUntilBase(predicate);
  await flush();
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useAuth.setState({ user: null });
  mocks.auditCount.mockResolvedValue({ total: 2 });
  mocks.auditQuery.mockResolvedValue([
    { id: 2, ts: 1700000001000, sessionId: null, assetId: null, source: "auth", kind: "login_failure", payload: { reason: "rejected" }, exitCode: null, durationMs: null },
    { id: 1, ts: 1700000000000, sessionId: null, assetId: null, source: "auth", kind: "admin_reset_user", payload: { target_user_id: "u-2" }, exitCode: null, durationMs: null },
  ]);
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("account audit records", () => {
  it("shows account kinds with their labels and filters by the account source", async () => {
    mounted = mount(createElement(AuditView));
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("登录失败"));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("管理员重置用户");
    expect(text).not.toContain("login_failure");
    expect([...mounted.container.querySelectorAll(".nx-badge")].map((b) => b.textContent)).toContain("账号");

    clickButton(mounted.container, "账号");
    await flushUntil(() => mocks.auditQuery.mock.calls.some(([args]) => (args as { source?: string }).source === "auth"));
    expect(mocks.auditQuery).toHaveBeenLastCalledWith(expect.objectContaining({ source: "auth", offset: 0 }));
  });
});
