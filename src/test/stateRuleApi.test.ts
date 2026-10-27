import { beforeEach, describe, expect, it, vi } from "vitest";

const call = vi.fn();
vi.mock("../ipc/commands", () => ({ call: (...args: unknown[]) => call(...args) }));

import { stateRuleApi } from "../ipc/stateRuleApi";

beforeEach(() => {
  call.mockReset();
  call.mockResolvedValue("result");
});

describe("stateRuleApi", () => {
  it("list, export, status call their commands without payload", async () => {
    await expect(stateRuleApi.list()).resolves.toBe("result");
    await stateRuleApi.export();
    await stateRuleApi.status();
    expect(call.mock.calls).toEqual([["state_rule_list"], ["state_rule_export"], ["state_rule_status"]]);
  });

  it("save and import spread the request into a fresh payload", async () => {
    const rule = { id: "r1", name: "n" } as never;
    const file = { content: "x" } as never;
    await stateRuleApi.save(rule);
    await stateRuleApi.import(file);
    expect(call).toHaveBeenNthCalledWith(1, "state_rule_save", rule);
    expect(call.mock.calls[0][1]).not.toBe(rule);
    expect(call).toHaveBeenNthCalledWith(2, "state_rule_import", file);
  });

  it("delete resolves to undefined whatever the backend returns", async () => {
    await expect(stateRuleApi.delete("r1")).resolves.toBeUndefined();
    expect(call).toHaveBeenCalledWith("state_rule_delete", { id: "r1" });
  });

  it("reorder sends the ids", async () => {
    await expect(stateRuleApi.reorder(["a", "b"])).resolves.toBe("result");
    expect(call).toHaveBeenCalledWith("state_rule_reorder", { ids: ["a", "b"] });
  });

  it("validate includes the screen only when given", async () => {
    const rule = { id: "r" } as never;
    const screen = { lines: [] } as never;
    await stateRuleApi.validate(rule);
    await stateRuleApi.validate(rule, screen);
    expect(call).toHaveBeenNthCalledWith(1, "state_rule_validate", { rule });
    expect(call).toHaveBeenNthCalledWith(2, "state_rule_validate", { rule, screen });
  });

  it("draft sends intent, current rule and screen", async () => {
    const screen = { lines: [] } as never;
    await expect(stateRuleApi.draft("not_this", "cur", screen)).resolves.toBe("result");
    expect(call).toHaveBeenCalledWith("state_rule_draft", { intent: "not_this", current: "cur", screen });
  });

  it("propagates backend errors", async () => {
    call.mockRejectedValue(new Error("boom"));
    await expect(stateRuleApi.list()).rejects.toThrow("boom");
  });
});
