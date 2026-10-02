import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWailsMock, type WailsMock } from "../wailsMock";

let runtime: WailsMock;

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
});

describe("terminal resize flush facade", () => {
  it("uses the dedicated command and top-level controller identity", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: null });
    const { terminalApi } = await import("../../ipc/commands");

    await terminalApi.resizeFlush("tab-1");

    expect(runtime.Call.ByName).toHaveBeenCalledWith("main.Service.Call", {
      cmd: "terminal_resize_flush",
      args: { tabId: "tab-1" },
      clientId: expect.any(String),
    });
  });

  it("keeps demo flush synchronous and rejects an observer like resize", async () => {
    vi.stubGlobal("window", {
      setTimeout: (callback: () => void, delay: number) => setTimeout(callback, delay),
    });
    const { mockInvoke } = await import("../../demo/mock");
    await mockInvoke("terminal_claim", { tabId: "t-bg-demo", clientId: "controller" });

    await expect(
      mockInvoke("terminal_resize_flush", { tabId: "t-bg-demo", clientId: "observer" }),
    ).rejects.toMatchObject({ code: "not_controller" });
    await expect(
      mockInvoke("terminal_resize_flush", { tabId: "t-bg-demo", clientId: "controller" }),
    ).resolves.toBeNull();
  });
});
