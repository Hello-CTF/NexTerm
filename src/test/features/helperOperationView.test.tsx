/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  HelperOperationView,
  type HelperOperationViewProps,
} from "../../features/terminal/HelperOperationView";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

const clipboard = vi.hoisted(() => ({ copyText: vi.fn() }));
vi.mock("../../ui/clipboard", () => clipboard);

function buttonByLabel(container: ParentNode, label: string): HTMLButtonElement {
  const button = container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!button) throw new Error(`Button not found: ${label}`);
  return button;
}

function liveText(container: ParentNode): string[] {
  return [...container.querySelectorAll('[aria-live="polite"]')].map((node) => node.textContent ?? "");
}

const LOCAL_SHA = "a".repeat(64);
const RUNNING_SHA = "b".repeat(64);
const OFFLINE_SHA = "c".repeat(64);

let mounted: MountedView | undefined;

beforeEach(() => {
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
});

function renderView(props: Partial<HelperOperationViewProps> = {}): MountedView {
  mounted = mount(<HelperOperationView stage="discover" {...props} />);
  return mounted;
}

function buttonByText(container: ParentNode, text: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
}

function definition(container: ParentNode, term: string): string {
  const element = [...container.querySelectorAll("dt")].find(
    (candidate) => candidate.textContent?.trim() === term,
  );
  if (!element) throw new Error(`Definition term not found: ${term}`);
  const value = element.nextElementSibling;
  return (value?.querySelector("[data-value]") ?? value)?.textContent?.trim() ?? "";
}

function terms(container: ParentNode): string[] {
  return [...container.querySelectorAll("dt")].map((node) => node.textContent?.trim() ?? "");
}

function differs(container: ParentNode, term: string): boolean {
  const element = [...container.querySelectorAll("dt")].find(
    (candidate) => candidate.textContent?.trim() === term,
  );
  if (!element) throw new Error(`Definition term not found: ${term}`);
  return element.parentElement?.getAttribute("data-differs") === "true";
}

function statusText(container: ParentNode): string {
  return container.querySelector('[role="status"], [role="alert"]')?.textContent?.trim() ?? "";
}

const IDENTICAL = {
  known: true,
  release: "1.2.9",
  commit: "b3fe0b57c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6",
  protocol: 2,
  sha256: "f".repeat(64),
};

function sectionByLabel(container: ParentNode, label: string): HTMLElement {
  const section = [...container.querySelectorAll("section")].find(
    (candidate) => candidate.getAttribute("aria-label") === label,
  );
  if (!section) throw new Error(`Section not found: ${label}`);
  return section;
}

function keyDown(target: Element, key: string): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
  });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

describe("HelperOperationView", () => {
  it("exposes stage, reason, attempt and deadline through labelled status semantics", () => {
    const view = renderView({
      stage: "upload",
      reason: "SFTP 不可用，改用 exec",
      attempt: 2,
      maxAttempts: 3,
      deadline: "2026-10-22T11:10:00.000Z",
    });

    const region = view.container.querySelector("section");
    const headingId = region?.getAttribute("aria-labelledby") ?? "";
    expect(document.getElementById(headingId)?.textContent).toBe("持久终端组件");
    expect(view.container.querySelector('[role="status"]')).not.toBeNull();
    expect(view.container.querySelector("details")?.open).toBe(false);
    expect([...view.container.querySelectorAll("dt")].every((node) => node.closest("details"))).toBe(true);
    expect(statusText(view.container)).toBe("上传持久终端组件");
    expect(definition(view.container, "原因")).toBe("SFTP 不可用，改用 exec");
    expect(definition(view.container, "尝试次数")).toBe("2/3");
    expect(definition(view.container, "截止时间")).toBe("2026-10-22T11:10:00.000Z");
  });

  it("shows differing versions side by side, marking only the fields that differ", () => {
    const view = renderView({
      localVersion: {
        known: true,
        release: "v1.4.0",
        commit: "local-commit",
        protocol: 7,
        sha256: LOCAL_SHA,
      },
      runningVersion: {
        known: true,
        release: "v1.3.9",
        commit: "running-commit",
        protocol: "7",
        sha256: RUNNING_SHA,
      },
      compatibility: "compatible",
    });

    const local = sectionByLabel(view.container, "本地携带版本");
    expect(definition(local, "发布版本")).toBe("v1.4.0");
    expect(definition(local, "提交")).toBe("local-commit");
    expect(definition(local, "协议")).toBe("7");
    expect(definition(local, "SHA-256")).toBe(LOCAL_SHA);

    const running = sectionByLabel(view.container, "远端运行版本");
    expect(definition(running, "发布版本")).toBe("v1.3.9");
    expect(definition(running, "提交")).toBe("running-commit");
    expect(definition(running, "协议")).toBe("7");
    expect(definition(running, "SHA-256")).toBe(RUNNING_SHA);
    for (const section of [local, running]) {
      expect(differs(section, "发布版本")).toBe(true);
      expect(differs(section, "提交")).toBe(true);
      expect(differs(section, "SHA-256")).toBe(true);
      expect(differs(section, "协议")).toBe(false);
    }
    expect(local.querySelectorAll("[data-differs]")).toHaveLength(3);
    expect(view.container.textContent).toContain("协议兼容，可继续使用。");
    expect(view.container.textContent).not.toContain("有可用更新");
  });

  it("offers copy buttons for differing commits and digests and announces the result", async () => {
    clipboard.copyText.mockReset().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("denied"));
    const view = renderView({
      localVersion: { known: true, release: "1.3.0", commit: "local-commit", protocol: 2, sha256: LOCAL_SHA },
      runningVersion: { known: true, release: "1.2.9", commit: "running-commit", protocol: 2, sha256: RUNNING_SHA },
      compatibility: "compatible",
    });
    const local = sectionByLabel(view.container, "本地携带版本");
    const running = sectionByLabel(view.container, "远端运行版本");
    expect(local.querySelectorAll("button")).toHaveLength(2);
    expect(running.querySelectorAll("button")).toHaveLength(2);

    click(buttonByLabel(local, "复制本地携带版本的提交"));
    await flush();
    expect(clipboard.copyText).toHaveBeenCalledWith("local-commit");
    expect(liveText(local)).toContain("已复制本地携带版本的提交");

    click(buttonByLabel(running, "复制远端运行版本的SHA-256"));
    await flush();
    expect(clipboard.copyText).toHaveBeenLastCalledWith(RUNNING_SHA);
    expect(liveText(running)).toContain("复制远端运行版本的SHA-256失败");
    expect(buttonByLabel(running, "复制远端运行版本的SHA-256").textContent).toBe("复制失败");
  });

  it("announces a repeated copy of the same value again and clears the announcement afterwards", async () => {
    vi.useFakeTimers();
    clipboard.copyText.mockReset().mockResolvedValue(undefined);
    const view = renderView({ stage: "ready", localVersion: IDENTICAL, runningVersion: IDENTICAL, compatibility: "compatible" });
    const version = sectionByLabel(view.container, "组件版本");
    const copyCommit = buttonByLabel(version, "复制提交");

    click(copyCommit);
    await flush();
    expect(liveText(version)).toContain("已复制提交");

    click(copyCommit);
    expect(liveText(version)).not.toContain("已复制提交");
    await flush();
    expect(liveText(version)).toContain("已复制提交");
    expect(clipboard.copyText).toHaveBeenCalledTimes(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(liveText(version)).not.toContain("已复制提交");
    expect(copyCommit.textContent).toBe("复制");
    vi.useRealTimers();
  });

  it("renders identical local and running versions once, with full identifiers behind a detail", () => {
    const view = renderView({
      stage: "ready",
      localVersion: IDENTICAL,
      runningVersion: { ...IDENTICAL, release: "v1.2.9", protocol: "2", sha256: IDENTICAL.sha256.toUpperCase() },
      compatibility: "compatible",
    });

    expect(() => sectionByLabel(view.container, "本地携带版本")).toThrow();
    expect(() => sectionByLabel(view.container, "远端运行版本")).toThrow();
    const version = sectionByLabel(view.container, "组件版本");
    expect(version.textContent).toContain("版本 1.2.9 · 提交 b3fe0b5 · 协议 2（本地与主机一致）");
    expect(view.container.textContent?.split("1.2.9")).toHaveLength(2);
    expect(view.container.textContent).not.toContain("兼容性");
    expect(view.container.textContent).not.toContain("发布版本不同");
    const full = version.querySelector("details");
    expect(full?.open).toBe(false);
    expect(definition(version, "提交")).toBe(IDENTICAL.commit);
    expect(definition(version, "SHA-256")).toBe(IDENTICAL.sha256);
    expect(buttonByText(version, "复制").getAttribute("aria-label")).toBe("复制提交");
  });

  it("keeps a ready, up-to-date helper to one status line without unknown placeholders", () => {
    const view = renderView({
      stage: "ready",
      reason: null,
      attempt: 1,
      maxAttempts: 3,
      deadline: "2026-10-22T11:10:00.000Z",
      operationId: null,
      path: null,
      transport: null,
      localVersion: IDENTICAL,
      runningVersion: IDENTICAL,
      compatibility: "compatible",
      update: { status: "current" },
    });

    expect(statusText(view.container)).toBe("持久终端已就绪，组件已是最新版本");
    expect(view.container.textContent).not.toContain("未知");
    expect(terms(view.container)).not.toEqual(expect.arrayContaining(["阶段"]));
    for (const term of ["原因", "尝试次数", "截止时间", "操作 ID", "路径", "传输"]) {
      expect(terms(view.container)).not.toContain(term);
    }
    expect(view.container.querySelector("details")?.open).toBe(false);
    expect(() => sectionByLabel(view.container, "组件更新")).toThrow();
    expect(view.container.textContent?.split("已是最新")).toHaveLength(2);
  });

  it("opens the diagnostics on failure and lists the reason, attempts and deadline", () => {
    const view = renderView({
      stage: "failed",
      reason: "启动超时",
      attempt: 3,
      maxAttempts: 3,
      deadline: "2026-10-22T11:10:00.000Z",
    });

    expect(statusText(view.container)).toBe("持久终端暂时不可用");
    expect(view.container.querySelector("details")?.open).toBe(true);
    expect(definition(view.container, "阶段")).toBe("持久终端组件操作失败");
    expect(definition(view.container, "原因")).toBe("启动超时");
    expect(definition(view.container, "尝试次数")).toBe("3/3");
    expect(definition(view.container, "截止时间")).toBe("2026-10-22T11:10:00.000Z");
  });

  it("omits a first attempt while a stage is still in progress", () => {
    const view = renderView({ stage: "upload", attempt: 1, maxAttempts: 3, reason: " " });
    expect(terms(view.container)).not.toContain("尝试次数");
    expect(terms(view.container)).not.toContain("原因");
  });

  it("states unverifiable versions once instead of listing unknown fields", () => {
    const view = renderView({
      localVersion: { known: false, release: "unverified-local" },
      runningVersion: { known: false, release: "unverified-running" },
      compatibility: "unknown",
    });

    expect(view.container.textContent).toContain("本地与主机上的组件版本均无法确认");
    expect(view.container.textContent).not.toContain("unverified-local");
    expect(view.container.textContent).not.toContain("unverified-running");
    expect(view.container.textContent).not.toContain("未知");
    expect(view.container.textContent).toContain("无法确认兼容性，不能推断可安全继续");
  });

  it("shows only the known side when the running version cannot be verified", () => {
    const view = renderView({
      localVersion: IDENTICAL,
      runningVersion: { known: false },
      compatibility: "unknown",
    });
    const local = sectionByLabel(view.container, "本地携带版本");
    expect(definition(local, "发布版本")).toBe("1.2.9");
    expect(local.querySelector("[data-differs]")).toBeNull();
    expect(sectionByLabel(view.container, "远端运行版本").textContent).toContain("无法确认");
    expect(terms(sectionByLabel(view.container, "远端运行版本"))).toEqual([]);
  });

  it("says an update is available when the protocol matches but the release differs", () => {
    const view = renderView({
      stage: "ready",
      localVersion: { ...IDENTICAL, release: "1.3.0" },
      runningVersion: IDENTICAL,
      compatibility: "compatible",
      update: { status: "available" },
    });
    expect(view.container.textContent).toContain("协议兼容，可继续使用；有可用更新。");
    expect(differs(sectionByLabel(view.container, "本地携带版本"), "发布版本")).toBe(true);
    expect(statusText(view.container)).toBe("持久终端已就绪");
    expect(sectionByLabel(view.container, "组件更新").textContent).toContain("有可用更新");
  });

  it("does not promise continuing on a compatible protocol when the operation failed", () => {
    const view = renderView({
      stage: "failed",
      reason: "启动超时",
      localVersion: { ...IDENTICAL, release: "1.3.0" },
      runningVersion: IDENTICAL,
      compatibility: "compatible",
    });
    expect(view.container.textContent).toContain("兼容性：协议兼容。");
    expect(view.container.textContent).not.toContain("可继续使用");
  });

  it("explains the consequence of an incompatible protocol", () => {
    const view = renderView({
      stage: "vendor_specific_stage",
      localVersion: { ...IDENTICAL, protocol: 3 },
      runningVersion: IDENTICAL,
      compatibility: "incompatible",
    });

    expect(statusText(view.container)).toBe("vendor_specific_stage");
    expect(differs(sectionByLabel(view.container, "本地携带版本"), "协议")).toBe(true);
    expect(view.container.textContent).toContain("协议不兼容（本地 3，主机 2）");
    expect(view.container.textContent).toContain("持久终端暂不可用");
    expect(view.container.textContent).toContain("不会自动替换");
    expect(view.container.textContent).toContain("在主机上手动结束旧的持久终端组件进程（其中运行的终端会话会随之结束），然后重新连接");
    expect(view.container.textContent).not.toContain("中断其中的进程并更新");
  });

  it("tells the user to stop an incompatible helper by hand when the update state is unknown", () => {
    const view = renderView({ compatibility: "incompatible", update: { status: "unknown" } });
    const update = sectionByLabel(view.container, "组件更新");
    expect(update.textContent).toContain("在主机上手动结束旧的持久终端组件进程");
    expect(update.textContent).not.toContain("可选择中断进程并更新");
    expect(update.querySelectorAll("button")).toHaveLength(0);
  });

  it.each(["__proto__", "constructor", "toString", "hasOwnProperty"])(
    "preserves prototype-named custom stage %s without throwing",
    (stage) => {
      let view: MountedView | undefined;
      expect(() => {
        view = renderView({ stage });
      }).not.toThrow();
      if (!view) throw new Error("Helper operation view was not mounted");
      expect(statusText(view.container)).toBe(stage);
    },
  );

  it("offers retry, warned ordinary and cancel controls without manufacturing an outcome", () => {
    const onRetryPersistent = vi.fn();
    const onContinueOrdinary = vi.fn();
    const onCancel = vi.fn();
    const view = renderView({
      stage: "failed",
      reason: "启动超时",
      onRetryPersistent,
      onContinueOrdinary,
      onCancel,
    });
    const before = view.container.textContent;

    expect(view.container.textContent).toContain("无法转到后台");
    expect(view.container.textContent).toContain("保留当前连接");
    click(buttonByText(view.container, "重试持久终端"));
    click(buttonByText(view.container, "继续使用普通终端"));
    click(buttonByText(view.container, "取消后台操作"));

    expect(onRetryPersistent).toHaveBeenCalledTimes(1);
    expect(onContinueOrdinary).toHaveBeenCalledTimes(1);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(view.container.textContent).toBe(before);
    expect(view.container.textContent).not.toContain("远端已终止");
    expect(view.container.textContent).not.toContain("恢复成功");
  });

  it("supports focused Enter and Space activation through real keyboard events", () => {
    const onRetryPersistent = vi.fn();
    const onContinueOrdinary = vi.fn();
    const onCancel = vi.fn();
    const view = renderView({
      stage: "failed",
      onRetryPersistent,
      onContinueOrdinary,
      onCancel,
    });

    const retry = buttonByText(view.container, "重试持久终端");
    retry.focus();
    expect(document.activeElement).toBe(retry);
    expect(keyDown(retry, "Enter").defaultPrevented).toBe(true);
    expect(onRetryPersistent).toHaveBeenCalledTimes(1);
    expect(onContinueOrdinary).not.toHaveBeenCalled();

    const ordinary = buttonByText(view.container, "继续使用普通终端");
    ordinary.focus();
    expect(document.activeElement).toBe(ordinary);
    expect(keyDown(ordinary, " ").defaultPrevented).toBe(true);
    expect(onContinueOrdinary).toHaveBeenCalledTimes(1);

    const cancel = buttonByText(view.container, "取消后台操作");
    cancel.focus();
    expect(document.activeElement).toBe(cancel);
    expect(keyDown(cancel, "Enter").defaultPrevented).toBe(true);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(view.container.textContent).not.toContain("操作成功");
  });

  it("keeps asset preferences out of the helper dialog", () => {
    const view = renderView({ stage: "failed", onContinueOrdinary: vi.fn() });
    expect(view.container.querySelector('input[type="checkbox"]')).toBeNull();
    expect(view.container.textContent).not.toContain("记住此资产");
  });

  it("describes restore failure as a new ordinary shell, never a restored or killed remote", () => {
    const onContinueOrdinary = vi.fn();
    const view = renderView({
      stage: "failed",
      restoreFailure: true,
      onContinueOrdinary,
    });
    const before = view.container.textContent;

    expect(buttonByText(view.container, "新建普通终端")).toBeDefined();
    expect(view.container.textContent).toContain("新的普通终端");
    expect(view.container.textContent).toContain("原会话不会被恢复");
    expect(view.container.textContent).not.toContain("已恢复原会话");
    expect(view.container.textContent).not.toContain("远端已终止");

    click(buttonByText(view.container, "新建普通终端"));
    expect(onContinueOrdinary).toHaveBeenCalledTimes(1);
    expect(view.container.textContent).toBe(before);
  });

  it("shows the full offline SHA and requires an explicit trust action", () => {
    const onConfirmOfflineTrust = vi.fn();
    const view = renderView({
      offlineTrust: {
        fileName: "nexterm-helper-linux-amd64",
        sha256: OFFLINE_SHA,
        protocol: 7,
        compatibility: "compatible",
      },
      onConfirmOfflineTrust,
    });
    const trust = sectionByLabel(view.container, "信任离线组件文件");
    const before = trust.textContent;

    expect(trust.textContent).toContain(OFFLINE_SHA);
    expect(trust.textContent).toContain("核对 SHA-256 且协议兼容");
    expect(trust.textContent).toContain("发布版本不同本身不代表不兼容");
    const confirm = buttonByText(trust, "确认 SHA-256 并信任离线文件");
    expect(confirm.disabled).toBe(false);
    click(confirm);
    expect(onConfirmOfflineTrust).toHaveBeenCalledTimes(1);
    expect(trust.textContent).toBe(before);
    expect(trust.textContent).not.toContain("信任成功");
  });

  it.each([
    { label: "missing SHA", sha256: null, compatibility: "compatible" as const },
    { label: "malformed SHA", sha256: "not-a-sha", compatibility: "compatible" as const },
    { label: "unknown compatibility", sha256: OFFLINE_SHA, compatibility: "unknown" as const },
    { label: "incompatible protocol", sha256: OFFLINE_SHA, compatibility: "incompatible" as const },
  ])("keeps offline trust disabled for $label", ({ sha256, compatibility }) => {
    const onConfirmOfflineTrust = vi.fn();
    const view = renderView({
      offlineTrust: { fileName: "raw-helper", sha256, protocol: 7, compatibility },
      onConfirmOfflineTrust,
    });
    const trust = sectionByLabel(view.container, "信任离线组件文件");
    const confirm = buttonByText(trust, "确认 SHA-256 并信任离线文件");

    expect(confirm.disabled).toBe(true);
    expect(keyDown(confirm, "Enter").defaultPrevented).toBe(false);
    expect(onConfirmOfflineTrust).not.toHaveBeenCalled();
    if (!sha256) expect(definition(trust, "SHA-256")).toBe("未知");
    if (compatibility === "unknown") expect(trust.textContent).toContain("兼容性未知");
    if (compatibility === "incompatible") expect(trust.textContent).toContain("协议不兼容");
  });

  it("keeps update choices explicit and does not imply process revival", () => {
    const onUpdate = vi.fn();
    const onDeferUpdate = vi.fn();
    const onInterruptAndUpdate = vi.fn();
    const view = renderView({
      update: { status: "available", detail: "检测到 2 个 PTY 和 1 个 exec" },
      onUpdate,
      onDeferUpdate,
      onInterruptAndUpdate,
    });
    const before = view.container.textContent;

    expect(view.container.textContent).toContain("检测到 2 个 PTY 和 1 个 exec");
    expect(view.container.textContent).toContain("更新会保留正在运行的终端");
    expect(view.container.textContent).toContain("如需中断进程，会再次确认");
    click(buttonByText(view.container, "更新组件"));
    click(buttonByText(view.container, "稍后更新"));
    click(buttonByText(view.container, "中断活动进程并更新"));
    expect(onUpdate).toHaveBeenCalledTimes(1);
    expect(onDeferUpdate).toHaveBeenCalledTimes(1);
    expect(onInterruptAndUpdate).toHaveBeenCalledTimes(1);
    expect(view.container.textContent).toBe(before);
    expect(view.container.textContent).not.toContain("原 shell 已恢复");
  });

  it("distinguishes deferred and interrupted update presentations", () => {
    const deferred = renderView({
      update: { status: "deferred", detail: "仍有活动 PTY" },
    });
    expect(deferred.container.textContent).toContain("更新已延后");
    expect(deferred.container.textContent).toContain("如需中断进程，会再次确认");
    deferred.unmount();
    mounted = undefined;

    const interrupted = renderView({ update: { status: "interrupted", updated: true } });
    expect(interrupted.container.textContent).toContain("已中断运行中的进程并完成更新");
    expect(interrupted.container.textContent).not.toContain("进程已复活");
  });

  it("asks for explicit consent showing the old helper's sessions before ending it", () => {
    const onReplaceLegacy = vi.fn();
    const view = renderView({
      compatibility: "incompatible",
      update: { status: "legacy" },
      legacy: {
        protocol: 2, pid: 4242, ptySessions: 2, execSessions: 1, activityKnown: true, ptyListed: false,
        release: "1.2.9", consentToken: "2:4242:3:1",
      },
      onReplaceLegacy,
    });
    const section = sectionByLabel(view.container, "旧版组件");
    expect(section.textContent).toContain("协议 v2");
    expect(section.textContent).toContain("版本 1.2.9");
    expect(section.textContent).toContain("进程 4242");
    expect(section.textContent).toContain("其中有 3 个会话在运行（终端 2 个，后台命令 1 个）");
    expect(section.textContent).toContain("会被终止且无法恢复");
    expect(section.textContent).toContain("输出记录保留为只读");
    expect(view.container.textContent).not.toContain("手动结束");

    const replace = buttonByText(section, "终止旧版组件并升级");
    expect(replace.disabled).toBe(true);
    click(replace);
    expect(onReplaceLegacy).not.toHaveBeenCalled();

    const consent = section.querySelector<HTMLInputElement>("input[type=checkbox]");
    if (!consent) throw new Error("consent checkbox missing");
    expect(consent.parentElement?.textContent).toContain("将终止这 3 个会话");
    click(consent);
    expect(replace.disabled).toBe(false);
    click(replace);
    expect(onReplaceLegacy).toHaveBeenCalledTimes(1);
  });

  it("keeps an uninspectable or partly counted old helper honest", () => {
    const listed = renderView({
      legacy: { protocol: 2, ptySessions: 2, execSessions: 0, activityKnown: false, ptyListed: true, consentToken: "2:7:2:2", stale: true },
      onReplaceLegacy: vi.fn(),
    });
    const section = sectionByLabel(listed.container, "旧版组件");
    expect(section.textContent).toContain("至少有 2 个终端会话");
    expect(section.textContent).toContain("后台命令数未知");
    expect(section.textContent).toContain("状态已变化");
    expect(section.textContent).toContain("全部会话");
    listed.unmount();
    mounted = undefined;

    const uninspected = renderView({
      legacy: { protocol: 2, ptySessions: 0, execSessions: 0, activityKnown: false, ptyListed: false },
      onReplaceLegacy: vi.fn(),
    });
    const blocked = sectionByLabel(uninspected.container, "旧版组件");
    expect(blocked.textContent).toContain("无法确认其中有多少会话");
    expect(blocked.textContent).toContain("不能在应用内升级");
    expect(blocked.querySelector("button")).toBeNull();
  });

  it("reports a completed replacement with the sessions it ended", () => {
    const view = renderView({ update: { status: "replaced", endedSessions: 3 } });
    expect(view.container.textContent).toContain("已终止旧版组件并启用新版持久终端组件，结束了 3 个会话");
  });
});
