/** @vitest-environment jsdom */
// Desktop read-only preview IPC wire contract: preview_share_create/active are RegisterNested on the Go side
// (internal/app/production/preview_relay.go), so args must be wrapped in one more args envelope;
// flat arguments are stripped by the nested layer and decode to zero values, producing a "tabId/ttlMs is required" error.
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { call: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, call: mocks.call };
});

import { remotePreviewApi } from "../../ipc/remoteApi";

afterEach(() => {
  vi.clearAllMocks();
});

describe("remotePreviewApi nested args wire shape", () => {
  it("create wraps non-empty tabId and ttlMs in an args envelope", async () => {
    mocks.call.mockResolvedValue({ token: "tok-1", url: "u", sessionId: "shell-1" });

    await remotePreviewApi.create("tab-1", 3_600_000);

    expect(mocks.call).toHaveBeenCalledWith("preview_share_create", {
      args: { tabId: "tab-1", ttlMs: 3_600_000 },
    });
  });

  it("active wraps a non-empty tabId in an args envelope", async () => {
    mocks.call.mockResolvedValue({ sessionId: "shell-1" });

    await remotePreviewApi.active("tab-1");

    expect(mocks.call).toHaveBeenCalledWith("preview_share_active", { args: { tabId: "tab-1" } });
  });
});
