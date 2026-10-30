import { describe, expect, it } from "vitest";
import { skillSummary } from "../../features/settings/skillText";
import type { SkillEntry } from "../../features/settings/skillsApi";

function entry(patch: Partial<SkillEntry>): SkillEntry {
  return {
    name: "incident-triage",
    description: "Read-only first-pass triage.",
    source: "embedded",
    overridden: false,
    disabled: false,
    digest: "d",
    updatedAt: 0,
    ...patch,
  };
}

describe("skillSummary", () => {
  it("shows a Chinese summary for a built-in skill", () => {
    expect(skillSummary(entry({}))).toMatch(/排查/);
  });

  it("shows the user's own description once it is overridden", () => {
    const override = { name: "incident-triage", description: "我的描述", updatedAt: 1 };
    expect(skillSummary(entry({ overridden: true, override, description: "我的描述" }))).toBe("我的描述");
  });

  it("keeps the description of a built-in without a summary and of custom skills", () => {
    expect(skillSummary(entry({ name: "unknown-builtin" }))).toBe("Read-only first-pass triage.");
    expect(skillSummary(entry({ source: "custom" as SkillEntry["source"], description: "自定义" }))).toBe("自定义");
  });
});
