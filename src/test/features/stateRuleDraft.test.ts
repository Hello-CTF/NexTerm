import { describe, expect, it } from "vitest";
import {
  STATE_RULE_EXPORT_FORMAT,
  STATE_RULE_EXPORT_VERSION,
  STATE_RULE_MAX_PATTERN_BYTES,
  STATE_RULE_MAX_RULES,
  defaultCorrection,
  formFromRule,
  heuristicDraft,
  parseRuleFile,
  quoteRe2,
  saveRequestFromForm,
  stateRuleTargets,
} from "../../features/session/stateRuleDraft";
import { stateMarkIntents } from "../../features/session/stateMarkRequests";

describe("heuristic state rule drafts", () => {
  it("quotes the last non-empty line of the bottom region", () => {
    const draft = heuristicDraft(
      { lines: ["$ deploy", "Overwrite build/app.js (1.2 MB)? [y/N]", "", ""], cursorRow: 2 },
      "this_is_it",
      "running",
    );
    expect(draft).toEqual({
      name: "Overwrite build/app.js (1.2 MB)? [y/N]",
      region: "bottom",
      lines: 5,
      pattern: "Overwrite build/app\\.js \\(1\\.2 MB\\)\\? \\[y/N\\]",
      state: "needs_me",
    });
  });

  it("widens the region to reach a line far above the cursor", () => {
    const lines = ["Waiting for review", ...Array.from({ length: 9 }, () => ""), "> "];
    const draft = heuristicDraft({ lines, cursorRow: 10 }, "not_this", "needs_me");
    expect(draft?.pattern).toBe(">");
    const above = heuristicDraft({ lines: [...lines.slice(0, 10), ""], cursorRow: 10 }, "not_this", "needs_me");
    expect(above?.pattern).toBe("Waiting for review");
    expect(above?.lines).toBe(11);
    expect(above?.state).toBe("running");
  });

  it("returns no draft for an empty screen", () => {
    expect(heuristicDraft({ lines: ["", "  "], cursorRow: 1 }, "this_is_it", "idle")).toBeNull();
  });

  it("keeps quoted patterns within the pattern limit for wide multi-byte rows", () => {
    const draft = heuristicDraft({ lines: ["确认？".repeat(200)], cursorRow: 0 }, "this_is_it", "idle");
    expect(new TextEncoder().encode(draft!.pattern).length).toBeLessThanOrEqual(STATE_RULE_MAX_PATTERN_BYTES);
  });

  it("escapes every RE2 metacharacter", () => {
    expect(quoteRe2("a.b*c+d?e(f)g[h]i{j}k|l^m$n\\o")).toBe("a\\.b\\*c\\+d\\?e\\(f\\)g\\[h\\]i\\{j\\}k\\|l\\^m\\$n\\\\o");
  });
});

describe("marking intents", () => {
  it("offers corrections only for states read from the screen", () => {
    expect(stateMarkIntents(undefined)).toEqual([]);
    expect(stateMarkIntents("unknown")).toEqual([]);
    expect(stateMarkIntents("exited")).toEqual([]);
    expect(stateMarkIntents("needs_me")).toEqual(["not_this"]);
    expect(stateMarkIntents("running")).toEqual(["not_this", "this_is_it"]);
    expect(stateMarkIntents("idle")).toEqual(["not_this", "this_is_it"]);
  });

  it("never proposes the state the user rejected", () => {
    expect(stateRuleTargets("this_is_it", "running")).toEqual(["needs_me"]);
    expect(stateRuleTargets("not_this", "needs_me")).not.toContain("needs_me");
    expect(defaultCorrection("not_this", "needs_me")).toBe("running");
    expect(defaultCorrection("not_this", "running")).toBe("idle");
  });
});

describe("rule forms", () => {
  it("drops keys and lines that do not apply to the rule", () => {
    const form = formFromRule({ id: "R", name: "n", scope: "global", region: "title", pattern: "x", state: "idle", keys: "y", enabled: true });
    expect(saveRequestFromForm(form)).toEqual({ id: "R", name: "n", scope: "global", region: "title", pattern: "x", state: "idle", enabled: true });
    const host = saveRequestFromForm({ ...form, scope: "host", assetId: "A", region: "bottom", lines: 3, state: "needs_me" });
    expect(host).toMatchObject({ scope: "host", assetId: "A", lines: 3, keys: "y" });
  });
});

describe("rule files", () => {
  const exported = {
    format: STATE_RULE_EXPORT_FORMAT,
    version: STATE_RULE_EXPORT_VERSION,
    rules: [{ id: "01J0RULE00000000000000000A", name: "review", scope: "global", region: "bottom", lines: 5, pattern: "Waiting", state: "needs_me", enabled: true, updatedAt: 1 }],
  };

  it("reads back what an export wrote", () => {
    expect(parseRuleFile(JSON.stringify(exported, null, 2))).toEqual(exported);
  });

  it("rejects files that are not rule exports or exceed the limits", () => {
    expect(() => parseRuleFile("{")).toThrow("不是有效的 JSON");
    expect(() => parseRuleFile(JSON.stringify({ ...exported, format: "other" }))).toThrow("不是可识别的状态规则文件");
    expect(() => parseRuleFile(JSON.stringify({ ...exported, rules: [{ name: "x" }] }))).toThrow("第 1 条规则格式不正确");
    const long = { ...exported.rules[0], pattern: "x".repeat(STATE_RULE_MAX_PATTERN_BYTES + 1) };
    expect(() => parseRuleFile(JSON.stringify({ ...exported, rules: [long] }))).toThrow("超过 512 字节");
    const many = Array.from({ length: STATE_RULE_MAX_RULES + 1 }, () => exported.rules[0]);
    expect(() => parseRuleFile(JSON.stringify({ ...exported, rules: many }))).toThrow("最多导入");
  });
});
