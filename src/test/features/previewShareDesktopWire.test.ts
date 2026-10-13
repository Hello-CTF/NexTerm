/** @vitest-environment jsdom */
// Desktop read-only preview IPC wire contract: preview_share_create/active are RegisterNested on the Go side
// (internal/app/production/preview_relay.go), so args must be wrapped in one more args envelope;
// flat arguments get stripped by the nested layer and decode to zero values, erroring with "tabId/ttlMs 不能为空".
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

describe("remotePreviewApi 嵌套 args 线形", () => {
  it("create 以 args 信封携带非空 tabId 与 ttlMs", async () => {
    mocks.call.mockResolvedValue({ token: "tok-1", url: "u", sessionId: "shell-1" });

    await remotePreviewApi.create("tab-1", 3_600_000);

    expect(mocks.call).toHaveBeenCalledWith("preview_share_create", {
      args: { tabId: "tab-1", ttlMs: 3_600_000 },
    });
  });

  it("active 以 args 信封携带非空 tabId", async () => {
    mocks.call.mockResolvedValue({ sessionId: "shell-1" });

    await remotePreviewApi.active("tab-1");

    expect(mocks.call).toHaveBeenCalledWith("preview_share_active", { args: { tabId: "tab-1" } });
  });
});
