import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWailsMock, type WailsMock } from "./wailsMock";

let runtime: WailsMock;

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
  vi.unstubAllGlobals();
});

describe("generated desktop bindings", () => {
  it("dispatches command calls through the generated Call export", async () => {
    runtime.Call.ByName.mockResolvedValue({ ok: true, data: "macos" });
    const { systemApi } = await import("../ipc/commands");

    await expect(systemApi.platform()).resolves.toBe("macos");

    const request = { cmd: "app_platform", args: null };
    expect(runtime.Call.ByID).toHaveBeenCalledWith(2349242617, request);
    expect(runtime.Call.ByName).toHaveBeenCalledWith("main.Service.Call", request);
  });

  it("consumes deep links through the generated ConsumeDeepLinks export", async () => {
    const links = ["nexterm://asset/one", "nexterm://asset/two"];
    runtime.Call.ByName.mockResolvedValue(links);
    const { consumeDeepLinks } = await import("../ipc/wails");

    await expect(consumeDeepLinks()).resolves.toEqual(links);

    expect(runtime.Call.ByID).toHaveBeenCalledWith(3490772380);
    expect(runtime.Call.ByName).toHaveBeenCalledWith("main.Service.ConsumeDeepLinks");
  });

  it("normalizes a null deep-link result without bypassing the generated export", async () => {
    runtime.Call.ByName.mockResolvedValue(null);
    const { consumeDeepLinks } = await import("../ipc/wails");

    await expect(consumeDeepLinks()).resolves.toEqual([]);

    expect(runtime.Call.ByID).toHaveBeenCalledWith(3490772380);
    expect(runtime.Call.ByName).toHaveBeenCalledWith("main.Service.ConsumeDeepLinks");
  });
});
