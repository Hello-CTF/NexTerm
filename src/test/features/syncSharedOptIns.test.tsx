/** @vitest-environment jsdom */
// Shared sync-content toggles section (features/settings/syncOptIns): the web and desktop "Account Sync" consoles
// both render SyncOptInSection; that module is the single source of the toggles' title/description/summary copy.
// This file pins that copy (the web version's existing wording) and asserts at source level that both SyncCard consoles
// use the shared component and no longer hardcode any toggle copy, keeping the two ends from diverging again.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";
import syncCardSource from "../../features/settings/SyncCard.tsx?raw";
import {
  SYNC_AUTO_BADGE,
  SYNC_CONSOLE_TITLE,
  SYNC_INTERVAL_LABEL,
  SYNC_MERGE_GUIDE,
  SYNC_OPT_IN_ITEMS,
  SyncOptInSection,
  syncOptInSummary,
} from "../../features/settings/syncOptIns";

const ALL_ON = { knownHost: true, aiProfile: true, aiPermission: true, aiGrant: true, preferences: true };
const MIXED = { knownHost: true, aiProfile: false, aiPermission: true, aiGrant: false, preferences: false };
const ALL_OFF = { knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false };

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
  it("renders the canonical title and description copy for all five toggles with exact web wording", async () => {
    mountSection();
    await flush();
    const labels = [...mounted!.container.querySelectorAll("label")];
    expect(labels.length).toBe(5);
    const rendered = labels.map((l) => l.textContent ?? "");
    expect(rendered[0]).toBe("同步已知主机（主机信任）默认开启。关闭后不再同步主机信任，也不会删除云端已有副本。");
    expect(rendered[1]).toBe("同步 AI 模型档案默认开启。关闭后不再同步模型档案；含 API 密钥的档案仅在凭据库解锁时同步。");
    expect(rendered[2]).toBe("同步 AI 权限与拦截规则默认开启。关闭后不再同步全局权限模式和危险命令拦截规则。");
    expect(rendered[3]).toBe("同步 AI 授权与权限模式默认开启。设备授权、路径授权规则与设备权限模式端到端加密同步；会话内临时授权不同步。");
    expect(rendered[4]).toBe("同步外观与快捷键默认开启。关闭后不再同步界面外观和快捷键覆盖；Cron 任务不同步。");
    // Rendered output matches the exported constants verbatim: both consoles get this exact same copy
    for (const [i, item] of SYNC_OPT_IN_ITEMS.entries()) {
      expect(rendered[i]).toBe(item.label + item.hint);
    }
  });

  it("renders the exact on/off summary for all five toggles", async () => {
    mountSection(MIXED);
    await flush();
    const summary = "已知主机 已开启 · AI 模型档案 已关闭 · AI 权限 已开启 · AI 授权 已关闭 · 外观快捷键 已关闭";
    expect(mounted!.container.textContent).toContain(summary);
    expect(syncOptInSummary(MIXED)).toBe(summary);
  });

  it("derives checked state from value and reports the corresponding key patch through onChange", async () => {
    const onChange = mountSection(MIXED);
    await flush();
    const boxes = [...mounted!.container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    expect(boxes.map((b) => b.checked)).toEqual([true, false, true, false, false]);
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
  });

  it("auto-expands when any toggle is enabled, defaults to collapsed when all are disabled, and expands from the sync content options button", async () => {
    mountSection(ALL_ON);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(5);
    mounted!.unmount();

    mountSection(ALL_OFF);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(0);
    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.includes("同步内容开关"))!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    click(toggle);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(5);
  });
});

describe("Both consoles render identical toggle copy without divergence", () => {
  it("uses the shared component and shared status-line constants in both SyncCard consoles", () => {
    expect(syncCardSource.match(/<SyncOptInSection/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_CONSOLE_TITLE\}/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_AUTO_BADGE\}/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_INTERVAL_LABEL\}/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_MERGE_GUIDE\}/g)?.length).toBe(2);
  });

  it("hardcodes no toggle copy in SyncCard and removes the legacy sync console title", () => {
    expect(syncCardSource).not.toContain("同步台");
    for (const item of SYNC_OPT_IN_ITEMS) {
      expect(syncCardSource).not.toContain(item.label);
      expect(syncCardSource).not.toContain(item.hint);
    }
  });

  it("shares unified title, status, and guide constants across both consoles", () => {
    expect(SYNC_CONSOLE_TITLE).toBe("账号同步");
    expect(SYNC_AUTO_BADGE).toBe("自动同步");
    expect(SYNC_INTERVAL_LABEL).toBe("每 60 秒");
    expect(SYNC_MERGE_GUIDE).toBe("数据会自动在本机与云端之间同步。");
  });

  it("distinguishes content-type opt-out from per-transcript cloud deletion in both consoles", () => {
    expect(syncCardSource.match(/关闭上方内容类型开关不会删除云端已有副本/g)?.length).toBe(2);
    expect(syncCardSource.match(/关闭单条录像同步会删除其云端副本，本机记录保留/g)?.length).toBe(2);
  });

  it("uses identical cloud object count copy on web and desktop, with desktop obtaining the count through sync_compare", () => {
    expect(syncCardSource.match(/云端 \{/g)?.length).toBe(2);
    expect(syncCardSource.match(/个对象/g)?.length).toBe(2);
  });
});
