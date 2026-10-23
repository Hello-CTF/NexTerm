import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (globalThis as unknown as Record<string, unknown>).window ??= globalThis;
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { callDesktop: vi.fn() };
});

vi.mock("../ipc/wails", () => ({ callDesktop: mocks.callDesktop }));

import { call, SYNC_WAKE_DELAY_MS } from "../ipc/commands";

function wakeCalls(): number {
  return mocks.callDesktop.mock.calls.filter(([cmd]) => cmd === "sync_wake").length;
}

beforeEach(() => {
  vi.useFakeTimers();
  mocks.callDesktop.mockReset();
  mocks.callDesktop.mockResolvedValue({});
});

afterEach(() => {
  vi.useRealTimers();
});

describe("sync wake after synced writes", () => {
  it("wakes one sync round after a burst of synced writes", async () => {
    await call("snippet_create" as never, { name: "a", body: "ls" } as never);
    await call("asset_update" as never, { id: "x" } as never);
    expect(wakeCalls()).toBe(0);
    await vi.advanceTimersByTimeAsync(SYNC_WAKE_DELAY_MS);
    expect(wakeCalls()).toBe(1);
  });

  it("does not wake for reads or failed writes", async () => {
    await call("snippet_list" as never);
    mocks.callDesktop.mockRejectedValueOnce(new Error("denied"));
    await expect(call("asset_delete" as never, { id: "x" } as never)).rejects.toBeTruthy();
    await vi.advanceTimersByTimeAsync(SYNC_WAKE_DELAY_MS * 2);
    expect(wakeCalls()).toBe(0);
  });
});
