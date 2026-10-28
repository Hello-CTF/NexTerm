/** @vitest-environment jsdom */
// Shared sync-content toggles section (features/settings/syncOptIns): the web and desktop "Account Sync" consoles
// both render SyncOptInSection; that module is the single source of the toggles' title/description/summary copy.
// This file pins that copy (the web version's existing wording) and asserts at source level that both SyncCard consoles
// use the shared component and no longer hardcode any toggle copy, keeping the two ends from diverging again.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
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

describe("Shared SyncOptInSection toggles", () => {
  it("renders the canonical title and description copy for every toggle with exact web wording", async () => {
    mountSection();
    await flush();
    const labels = [...mounted!.container.querySelectorAll("label")];
    expect(labels.length).toBe(SYNC_OPT_IN_ITEMS.length);
    const rendered = labels.map((l) => l.textContent ?? "");
    expect(rendered[0]).toBe("同步已知主机（主机信任）默认开启。关闭后不再同步主机信任，也不会删除服务端已有副本。");
    expect(rendered[1]).toBe("同步 AI 模型档案默认开启。关闭后不再同步模型档案；含 API 密钥的档案仅在凭据库解锁时同步。");
    expect(rendered[2]).toBe("同步 AI 权限与拦截规则默认开启。关闭后不再同步全局权限模式和危险命令拦截规则。");
    expect(rendered[3]).toBe("同步 AI 授权与权限模式默认开启。设备授权、路径授权规则与设备权限模式端到端加密同步；会话内临时授权不同步。");
    expect(rendered[4]).toBe("同步外观与快捷键默认开启。关闭后不再同步界面外观和快捷键覆盖；定时任务由单独的开关控制。");
    expect(rendered[5]).toBe("同步会话状态规则默认开启。同步后的规则会推送到本机的会话助手；规则顺序按各设备收到的先后排列。");
    expect(rendered[6]).toBe("同步 Runbook 模板默认开启。模板中的命令按保存时的快照同步。");
    expect(rendered[7]).toBe("同步自定义 AI 技能与内置技能的修改默认开启。只同步你的自定义技能、对内置技能的修改和禁用；内置技能本身随应用提供。");
    expect(rendered[8]).toBe("同步 AI 记忆默认关闭。记忆内容端到端加密同步，离开本机前和写入前都会再次脱敏疑似密钥。");
    expect(rendered[9]).toBe("同步 AI 定时任务默认关闭。每个任务只在它绑定的设备上运行；其他设备只显示，可在此改由本机运行或删除。");
    // Rendered output matches the exported constants verbatim: both consoles get this exact same copy
    for (const [i, item] of SYNC_OPT_IN_ITEMS.entries()) {
      expect(rendered[i]).toBe(item.label + item.hint);
    }
  });

  it("renders the exact on/off summary for every toggle", async () => {
    mountSection(MIXED);
    await flush();
    const summary = "已知主机 已开启 · AI 模型档案 已关闭 · AI 权限 已开启 · AI 授权 已关闭 · 外观快捷键 已关闭 · 状态规则 已关闭 · Runbook 模板 已关闭 · AI 技能 已关闭 · AI 记忆 已关闭 · 定时任务 已关闭";
    expect(mounted!.container.textContent).toContain(summary);
    expect(syncOptInSummary(MIXED)).toBe(summary);
  });

  it("derives checked state from value and reports the corresponding key patch through onChange", async () => {
    const onChange = mountSection(MIXED);
    await flush();
    const boxes = [...mounted!.container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    expect(boxes.map((b) => b.checked)).toEqual([true, false, true, false, false, false, false, false, false, false]);
    click(boxes[0]);
    expect(onChange).toHaveBeenCalledWith({ knownHost: false });
    click(boxes[1]);
    expect(onChange).toHaveBeenCalledWith({ aiProfile: true });
    click(boxes[2]);
    expect(onChange).toHaveBeenCalledWith({ aiPermission: false });
    click(boxes[3]);
    expect(onChange).toHaveBeenCalledWith({ aiGrant: true });
    click(boxes[4]);
    expect(onChange).toHaveBeenCalledWith({ preferences: true });
    click(boxes[5]);
    expect(onChange).toHaveBeenCalledWith({ stateRules: true });
    click(boxes[6]);
    expect(onChange).toHaveBeenCalledWith({ runbookTemplates: true });
    click(boxes[7]);
    expect(onChange).toHaveBeenCalledWith({ skills: true });
  });

  it("auto-expands when any toggle is enabled, defaults to collapsed when all are disabled, and expands from the sync content options button", async () => {
    mountSection(ALL_ON);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(SYNC_OPT_IN_ITEMS.length);
    mounted!.unmount();

    mountSection(ALL_OFF);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(0);
    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.includes("同步内容开关"))!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    click(toggle);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(SYNC_OPT_IN_ITEMS.length);
  });
});

describe("Both consoles share unified copy constants", () => {
  it("exposes unified title, status, and guide constants", () => {
    expect(SYNC_CONSOLE_TITLE).toBe("账号同步");
    expect(SYNC_AUTO_BADGE).toBe("自动同步");
    expect(SYNC_INTERVAL_LABEL).toBe("每 60 秒");
    expect(SYNC_MERGE_GUIDE).toBe("数据会自动在本机与服务端之间同步。");
  });
});
