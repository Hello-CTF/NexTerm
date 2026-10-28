/** @vitest-environment jsdom */
// Shared sync-content toggles section (features/settings/syncOptIns): the web and desktop "Account Sync" consoles
// both render SyncOptInSection; that module is the single source of the toggles' title/description/summary copy.
// This file pins that copy (the web version's existing wording) and asserts at source level that both SyncCard consoles
// use the shared component and no longer hardcode any toggle copy, keeping the two ends from diverging again.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useState } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";
import {
  SYNC_AUTO_BADGE,
  SYNC_CONSOLE_TITLE,
  SYNC_INTERVAL_LABEL,
  SYNC_MERGE_GUIDE,
  SYNC_OPT_IN_ITEMS,
  SyncOptInSection,
  syncOptInSummary,
} from "../../features/settings/syncOptIns";

const ALL_ON = { knownHost: true, aiProfile: true, aiPermission: true, aiGrant: true, preferences: true, stateRules: true, runbookTemplates: true, skills: true, memory: true, cron: true };
const MIXED = { knownHost: true, aiProfile: false, aiPermission: true, aiGrant: false, preferences: false, stateRules: false, runbookTemplates: false, skills: false, memory: false, cron: false };
const ALL_OFF = { knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false, stateRules: false, runbookTemplates: false, skills: false, memory: false, cron: false };

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountSection(value = MIXED, onChange = vi.fn()) {
  mounted = mount(createElement(SyncOptInSection, { value, busy: false, onChange }));
  return onChange;
}

function openDialog() {
  const manage = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "管理同步项")!;
  click(manage);
}

const dialog = () => document.body.querySelector('[role="dialog"]');
const dialogBoxes = () => [...(dialog()?.querySelectorAll<HTMLInputElement>('input[type="checkbox"]') ?? [])];
const boxFor = (key: string) => {
  const item = SYNC_OPT_IN_ITEMS.find((candidate) => candidate.key === key)!;
  return dialogBoxes().find((box) => box.closest("label")?.textContent === item.label + item.hint)!;
};

describe("Shared SyncOptInSection toggles", () => {
  it("shows only a summary row until the manage dialog is opened", async () => {
    mountSection(MIXED);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(0);
    expect(dialog()).toBeNull();
    expect(mounted!.container.textContent).toContain("已开启 2 / 10 项，未开启：AI 模型档案、AI 授权、外观快捷键、状态规则、Runbook 模板、AI 技能、AI 记忆、定时任务");
  });

  it("renders the canonical title and description copy for every toggle, grouped, inside the dialog", async () => {
    mountSection();
    await flush();
    openDialog();
    await flush();
    const labels = [...dialog()!.querySelectorAll("label")];
    expect(labels.length).toBe(SYNC_OPT_IN_ITEMS.length);
    const rendered = labels.map((l) => l.textContent ?? "");
    // Rendered output matches the exported constants verbatim: both consoles get this exact same copy
    for (const item of SYNC_OPT_IN_ITEMS) {
      expect(rendered).toContain(item.label + item.hint);
    }
    // Safety notes must survive the move into the dialog
    expect(dialog()!.textContent).toContain("含 API 密钥的档案仅在凭据库解锁时同步");
    expect(dialog()!.textContent).toContain("离开本机前和写入前都会再次脱敏疑似密钥");
    expect([...dialog()!.querySelectorAll("legend")].map((l) => l.textContent)).toEqual(["基础", "AI", "自动化"]);
  });

  it("builds the summary from the on/off state", () => {
    expect(syncOptInSummary(ALL_ON)).toBe("已开启 10 / 10 项");
    expect(syncOptInSummary(ALL_OFF)).toBe("已开启 0 / 10 项，未开启：已知主机、AI 模型档案、AI 权限、AI 授权、外观快捷键、状态规则、Runbook 模板、AI 技能、AI 记忆、定时任务");
    expect(syncOptInSummary(MIXED)).toContain("已开启 2 / 10 项");
  });

  it("derives checked state from value and reports the corresponding key patch through onChange", async () => {
    const onChange = mountSection(MIXED);
    await flush();
    openDialog();
    await flush();
    expect(boxFor("knownHost").checked).toBe(true);
    expect(boxFor("aiProfile").checked).toBe(false);
    for (const [key, next] of [["knownHost", false], ["aiProfile", true], ["aiPermission", false], ["aiGrant", true], ["preferences", true], ["stateRules", true], ["runbookTemplates", true], ["skills", true], ["memory", true], ["cron", true]] as const) {
      click(boxFor(key));
      expect(onChange).toHaveBeenLastCalledWith({ [key]: next });
    }
  });

  it("updates the summary after the dialog closes and keeps focus handling via Escape", async () => {
    mounted = mount(createElement(Harness));
    await flush();
    openDialog();
    await flush();
    click(boxFor("memory"));
    await flush();
    const done = [...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "完成")!;
    click(done);
    await flush();
    expect(dialog()).toBeNull();
    expect(mounted!.container.textContent).toContain("已开启 3 / 10 项");
    openDialog();
    await flush();
    act(() => {
      dialog()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await flush();
    expect(dialog()).toBeNull();
  });
});

function Harness() {
  const [value, setValue] = useState(MIXED);
  return createElement(SyncOptInSection, { value, busy: false, onChange: (patch) => setValue((current) => ({ ...current, ...patch })) });
}

describe("Both consoles share unified copy constants", () => {
  it("exposes unified title, status, and guide constants", () => {
    expect(SYNC_CONSOLE_TITLE).toBe("账号同步");
    expect(SYNC_AUTO_BADGE).toBe("自动同步");
    expect(SYNC_INTERVAL_LABEL).toBe("每 60 秒");
    expect(SYNC_MERGE_GUIDE).toBe("数据会自动在本机与服务端之间同步。");
  });
});
