import { describe, expect, it, vi } from "vitest";
import * as actual from "../ipc/commands";
import { createCommandsMock } from "./helpers/mockCommands";

describe("createCommandsMock", () => {
  it("returns provided methods unchanged and routes calls to them", async () => {
    const get = vi.fn(async () => ({ revision: 1, updatedAt: 0, data: null }));
    const mock = createCommandsMock(actual, { layoutApi: { get } });

    expect(mock.layoutApi.get).toBe(get);
    await expect(mock.layoutApi.get()).resolves.toEqual({ revision: 1, updatedAt: 0, data: null });
    expect(get).toHaveBeenCalledTimes(1);
  });

  it("fails with a precise error when an un-mocked method of a provided API is called", () => {
    const mock = createCommandsMock(actual, { sessionApi: { disconnect: vi.fn() } });

    expect(() => mock.sessionApi.list()).toThrowError(
      '[mockCommands] sessionApi.list() is not mocked in this test; provide it in the vi.mock("../../ipc/commands") factory.',
    );
  });

  it("fails with a precise error naming the API when a method of an unprovided API is called", () => {
    const mock = createCommandsMock(actual, {});

    expect(() => mock.layoutApi.put("{}", 0)).toThrowError(/layoutApi\.put\(\) is not mocked/);
    expect(() => mock.dbApi.disconnect("conn-1")).toThrowError(/dbApi\.disconnect\(\) is not mocked/);
  });

  it("fails with a precise error when an unprovided top-level function is called", () => {
    const mock = createCommandsMock(actual, {});

    expect(() => mock.call("session_list")).toThrowError(/\[mockCommands\] call\(\) is not mocked/);
  });

  it("uses a provided top-level function instead of the throwing stub", async () => {
    const call = vi.fn(async () => []);
    const mock = createCommandsMock(actual, { call });

    await expect(mock.call("session_list")).resolves.toEqual([]);
    expect(call).toHaveBeenCalledWith("session_list");
  });

  it("rejects a spec naming an export the real module does not have", () => {
    expect(() => createCommandsMock(actual, { layoutAp: {} } as never)).toThrowError(
      '[mockCommands] mock spec mentions unknown export "layoutAp".',
    );
  });

  it("rejects a spec naming a method the real API does not have", () => {
    expect(() =>
      createCommandsMock(actual, { layoutApi: { putt: vi.fn() } } as never),
    ).toThrowError('[mockCommands] mock spec mentions unknown method "layoutApi.putt".');
  });
});
