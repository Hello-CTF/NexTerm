import { beforeEach, describe, expect, it, vi } from "vitest";

const call = vi.hoisted(() => vi.fn());
vi.mock("../ipc/commands", () => ({ call }));

import { stateRuleApi, type StateRuleSaveRequest, type StateRuleScreenDTO } from "../ipc/stateRuleApi";

const rule: StateRuleSaveRequest = { name: "Waiting prompt", scope: "all", region: "tail", pattern: "Continue?", state: "needs_input", enabled: true };
const screen: StateRuleScreenDTO = { lines: ["Continue?"], cursorRow: 0, title: "shell" };

beforeEach(() => call.mockReset());

describe("session state rule IPC", () => {
  it("returns list, helper status and export data from their distinct commands", async () => {
    const rules = [{ ...rule, id: "rule-1" }];
    const status = { available: true };
    const file = { format: "nexterm-state-rules", version: 1, rules: [] };
    call.mockResolvedValueOnce(rules).mockResolvedValueOnce(status).mockResolvedValueOnce(file);
    await expect(stateRuleApi.list()).resolves.toBe(rules);
    await expect(stateRuleApi.status()).resolves.toBe(status);
    await expect(stateRuleApi.export()).resolves.toBe(file);
    expect(call.mock.calls).toEqual([["state_rule_list"], ["state_rule_status"], ["state_rule_export"]]);
  });

  it("preserves edits, import payload and explicit rule ordering", async () => {
    const saved = { ...rule, id: "rule-1" };
    call.mockResolvedValueOnce(saved);
    await expect(stateRuleApi.save(rule)).resolves.toBe(saved);
    expect(call).toHaveBeenLastCalledWith("state_rule_save", rule);
    await stateRuleApi.reorder(["rule-2", "rule-1"]);
    expect(call).toHaveBeenLastCalledWith("state_rule_reorder", { ids: ["rule-2", "rule-1"] });
    const file = { format: "nexterm-state-rules", version: 1, rules: [] };
    const result = { created: 0, updated: 0, unchanged: 0 };
    call.mockResolvedValueOnce(result);
    await expect(stateRuleApi.import(file)).resolves.toBe(result);
    expect(call).toHaveBeenLastCalledWith("state_rule_import", file);
  });

  it("validates with an optional screen and drafts without saving", async () => {
    const valid = { valid: true };
    call.mockResolvedValue(valid);
    await expect(stateRuleApi.validate(rule)).resolves.toBe(valid);
    expect(call).toHaveBeenLastCalledWith("state_rule_validate", { rule });
    await stateRuleApi.validate(rule, screen);
    expect(call).toHaveBeenLastCalledWith("state_rule_validate", { rule, screen });
    await stateRuleApi.draft("not_this", "working", screen);
    expect(call).toHaveBeenLastCalledWith("state_rule_draft", { intent: "not_this", current: "working", screen });
    expect(call.mock.calls.some(([command]) => command === "state_rule_save")).toBe(false);
  });

  it("waits for deletion and propagates backend failures to the caller", async () => {
    const failure = new Error("rule is in use");
    call.mockRejectedValueOnce(failure);
    await expect(stateRuleApi.delete("rule-1")).rejects.toBe(failure);
    expect(call).toHaveBeenLastCalledWith("state_rule_delete", { id: "rule-1" });
    call.mockResolvedValueOnce({});
    await expect(stateRuleApi.delete("rule-1")).resolves.toBeUndefined();
  });
});
