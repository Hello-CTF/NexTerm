/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  HelperOperationView,
  type HelperOperationViewProps,
} from "../../features/terminal/HelperOperationView";
import { click, mount, type MountedView } from "./reactTestUtils";

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
  return element.nextElementSibling?.textContent?.trim() ?? "";
}

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
    expect(document.getElementById(headingId)?.textContent).toBe("持久终端助手");
    expect(view.container.querySelector('[role="status"]')).not.toBeNull();
    expect(view.container.querySelector("details")?.open).toBe(false);
    expect([...view.container.querySelectorAll("dt")].every((node) => node.closest("details"))).toBe(true);
    expect(definition(view.container, "阶段")).toBe("上传持久终端助手");
    expect(definition(view.container, "原因")).toBe("SFTP 不可用，改用 exec");
    expect(definition(view.container, "尝试次数")).toBe("2/3");
    expect(definition(view.container, "截止时间")).toBe("2026-10-22T11:10:00.000Z");
  });

  it("presents local and running metadata without treating a release difference as incompatibility", () => {
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
    expect(view.container.textContent).toContain("协议兼容；发布版本不同不妨碍继续");
  });

  it("keeps unprovable version metadata and compatibility explicitly unknown", () => {
    const view = renderView({
      reason: null,
      attempt: null,
      deadline: null,
      localVersion: { known: false, release: "unverified-local" },
      runningVersion: { known: false, release: "unverified-running" },
      compatibility: "unknown",
    });

    expect(definition(view.container, "原因")).toBe("未知");
    expect(definition(view.container, "尝试次数")).toBe("未知");
    expect(definition(view.container, "截止时间")).toBe("未知");
    for (const label of ["本地携带版本", "远端运行版本"]) {
      const section = sectionByLabel(view.container, label);
      expect(section.textContent).toContain("元数据未知");
      expect(definition(section, "发布版本")).toBe("未知");
      expect(definition(section, "提交")).toBe("未知");
      expect(definition(section, "协议")).toBe("未知");
      expect(definition(section, "SHA-256")).toBe("未知");
    }
    expect(view.container.textContent).not.toContain("unverified-local");
    expect(view.container.textContent).not.toContain("unverified-running");
    expect(view.container.textContent).toContain("兼容性未知，不能推断可安全继续");
  });

  it("preserves unknown custom stages and identifies incompatible protocols", () => {
    const view = renderView({
      stage: "vendor_specific_stage",
      localVersion: null,
      runningVersion: null,
      compatibility: "incompatible",
    });

    expect(definition(view.container, "阶段")).toBe("vendor_specific_stage");
    expect(view.container.textContent).toContain("协议不兼容；不会替换正在运行的助手");
  });

  it.each(["__proto__", "constructor", "toString", "hasOwnProperty"])(
    "preserves prototype-named custom stage %s without throwing",
    (stage) => {
      let view: MountedView | undefined;
      expect(() => {
        view = renderView({ stage });
      }).not.toThrow();
      if (!view) throw new Error("Helper operation view was not mounted");
      expect(definition(view.container, "阶段")).toBe(stage);
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
    const trust = sectionByLabel(view.container, "离线 helper 信任");
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
    const trust = sectionByLabel(view.container, "离线 helper 信任");
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
    click(buttonByText(view.container, "更新助手"));
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
    expect(interrupted.container.textContent).toContain("已中断活动进程并更新助手");
    expect(interrupted.container.textContent).not.toContain("进程已复活");
  });
});
