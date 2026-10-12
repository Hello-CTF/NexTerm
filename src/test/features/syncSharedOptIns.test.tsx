/** @vitest-environment jsdom */
// 共享同步内容开关区 (features/settings/syncOptIns): 网页版与桌面版「账号同步」控制台
// 共同渲染 SyncOptInSection, 四路开关的标题/说明/摘要唯一文案来源就是该模块。
// 本文件钉住这份文案 (网页版既有措辞), 并源码级断言 SyncCard 两处控制台都走共享组件、
// 不再硬编码任一路开关文案, 防止两端再次写分叉。
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

const ALL_ON = { knownHost: true, aiProfile: true, aiPermission: true, preferences: true };
const MIXED = { knownHost: true, aiProfile: false, aiPermission: true, preferences: false };
const ALL_OFF = { knownHost: false, aiProfile: false, aiPermission: false, preferences: false };

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountSection(value = MIXED, onChange = vi.fn()) {
  mounted = mount(createElement(SyncOptInSection, { value, busy: false, onChange }));
  return onChange;
}

describe("SyncOptInSection 共享开关区", () => {
  it("渲染唯一文案清单: 四路开关的标题与说明逐字为网页版措辞", async () => {
    mountSection();
    await flush();
    const labels = [...mounted!.container.querySelectorAll("label")];
    expect(labels.length).toBe(4);
    const rendered = labels.map((l) => l.textContent ?? "");
    expect(rendered[0]).toBe("同步已知主机（主机信任）默认开启。关闭后不再同步主机信任，也不会删除云端已有副本。");
    expect(rendered[1]).toBe("同步 AI 模型档案默认开启。关闭后不再同步模型档案；API 密钥仅在凭据库解锁时随档案同步。");
    expect(rendered[2]).toBe("同步 AI 权限与拦截规则默认开启。关闭后不再同步全局权限模式和危险命令拦截规则。");
    expect(rendered[3]).toBe("同步外观与快捷键默认开启。关闭后不再同步界面外观和快捷键覆盖；Cron 任务不同步。");
    // 渲染结果与导出常量逐字一致: 两个控制台拿到的就是这同一份
    for (const [i, item] of SYNC_OPT_IN_ITEMS.entries()) {
      expect(rendered[i]).toBe(item.label + item.hint);
    }
  });

  it("摘要行逐字渲染四路开/关状态", async () => {
    mountSection(MIXED);
    await flush();
    const summary = "已知主机 已开启 · AI 模型档案 已关闭 · AI 权限 已开启 · 外观快捷键 已关闭";
    expect(mounted!.container.textContent).toContain(summary);
    expect(syncOptInSummary(MIXED)).toBe(summary);
  });

  it("勾选状态来自 value, 切换经 onChange 上报对应 key 的 patch", async () => {
    const onChange = mountSection(MIXED);
    await flush();
    const boxes = [...mounted!.container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    expect(boxes.map((b) => b.checked)).toEqual([true, false, true, false]);
    click(boxes[0]);
    expect(onChange).toHaveBeenCalledWith({ knownHost: false });
    click(boxes[1]);
    expect(onChange).toHaveBeenCalledWith({ aiProfile: true });
    click(boxes[2]);
    expect(onChange).toHaveBeenCalledWith({ aiPermission: false });
    click(boxes[3]);
    expect(onChange).toHaveBeenCalledWith({ preferences: true });
  });

  it("有任一路开启时面板自动展开; 全部关闭时默认折叠, 点「同步内容开关」展开", async () => {
    mountSection(ALL_ON);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(4);
    mounted!.unmount();

    mountSection(ALL_OFF);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(0);
    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.includes("同步内容开关"))!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    click(toggle);
    await flush();
    expect(mounted!.container.querySelectorAll('input[type="checkbox"]').length).toBe(4);
  });
});

describe("两个控制台渲染同一份开关文案(防分叉)", () => {
  it("SyncCard 两处控制台都引用共享组件与共享状态行常量", () => {
    expect(syncCardSource.match(/<SyncOptInSection/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_CONSOLE_TITLE\}/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_AUTO_BADGE\}/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_INTERVAL_LABEL\}/g)?.length).toBe(2);
    expect(syncCardSource.match(/\{SYNC_MERGE_GUIDE\}/g)?.length).toBe(2);
  });

  it("SyncCard 不再硬编码任一路开关文案, 也不再有旧「同步台」标题", () => {
    expect(syncCardSource).not.toContain("同步台");
    for (const item of SYNC_OPT_IN_ITEMS) {
      expect(syncCardSource).not.toContain(item.label);
      expect(syncCardSource).not.toContain(item.hint);
    }
  });

  it("标题/状态行/引导句常量的值即两个控制台的统一文案", () => {
    expect(SYNC_CONSOLE_TITLE).toBe("账号同步");
    expect(SYNC_AUTO_BADGE).toBe("自动同步");
    expect(SYNC_INTERVAL_LABEL).toBe("每 60 秒");
    expect(SYNC_MERGE_GUIDE).toBe("登录并解锁数据密钥后，系统会先拉取并合并云端内容，再推送本机变更。");
  });

  it("云端对象计数只有网页版一处(桌面拿不到计数时如实不显示, 不另写近似文案)", () => {
    expect(syncCardSource.match(/个对象/g)?.length).toBe(1);
  });
});
