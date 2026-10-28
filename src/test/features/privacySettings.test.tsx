/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";
import type { PrivacyStatus } from "../../ipc/privacy";

const mocks = vi.hoisted(() => ({ status: vi.fn(), configure: vi.fn(), download: vi.fn(), checkUpdates: vi.fn(), cancelDownload: vi.fn() }));
vi.mock("../../ipc/commands", () => ({
  call: (command: string, args: Record<string, unknown>) => {
    switch (command) {
      case "ai_privacy_status": return mocks.status();
      case "ai_privacy_configure": return mocks.configure(args);
      case "ai_privacy_download": return mocks.download(args.precision);
      case "ai_privacy_check_updates": return mocks.checkUpdates();
      case "ai_privacy_cancel_download": return mocks.cancelDownload();
      default: throw new Error(`Unexpected command: ${command}`);
    }
  },
}));
vi.mock("../../ipc/env", () => ({ WEB: true }));
vi.mock("../../features/auth/store", () => ({ useAuth: (select: (value: unknown) => unknown) => select({ user: { id: "alice" } }) }));
import { PrivacyCard } from "../../features/settings/PrivacyCard";

const baseline = (): PrivacyStatus => ({
  config: { enabled: false, precision: "int8", revision: "", version: "" },
  installed: [],
  download: { running: false, precision: "int8", progress: { file: "", downloaded: 0, total: 0 } },
});
let mounted: MountedView | undefined;
beforeEach(() => { vi.resetAllMocks(); mocks.status.mockResolvedValue(baseline()); });
afterEach(() => { mounted?.unmount(); mounted = undefined; });
function button(label: string): HTMLButtonElement {
  const found = [...mounted!.container.querySelectorAll("button")].find((item) => item.textContent === label);
  if (!found) throw new Error(`Missing button: ${label}`);
  return found;
}

describe("AI privacy settings", () => {
  it("starts disabled, explains the server location, and downloads the selected precision without enabling", async () => {
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('input[type="checkbox"]')).not.toBeNull());
    const toggle = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(toggle.checked).toBe(false);
    expect(toggle.disabled).toBe(true);
    expect(mounted.container.textContent).toContain("当前 NexTerm 服务端");
    const precision = mounted.container.querySelector<HTMLSelectElement>('[aria-label="下载模型精度"]')!;
    act(() => { precision.value = "fp32"; precision.dispatchEvent(new Event("change", { bubbles: true })); });
    mocks.download.mockResolvedValue({ ...baseline(), download: { ...baseline().download, running: true, precision: "fp32" } });
    click(button("下载最新模型"));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("正在获取模型版本"));
    expect(mocks.download).toHaveBeenCalledWith("fp32");
    expect(mocks.configure).not.toHaveBeenCalled();
    expect(toggle.checked).toBe(false);
    mocks.cancelDownload.mockResolvedValue(undefined);
    click(button("取消下载"));
    await waitForRendered(() => expect(mounted!.container.querySelector("progress")).toBeNull());
    expect(mocks.cancelDownload).toHaveBeenCalledOnce();
    expect(mocks.configure).not.toHaveBeenCalled();
  });

  it("retains the disabled state and shows a native runtime failure when enabling fails", async () => {
    const state = baseline();
    state.config = { enabled: false, precision: "int8", revision: "a".repeat(40), version: "v0.1.3" };
    state.installed = [{ ...state.config, files: { "model.onnx": { size: 1024, sha256: "a".repeat(64) } } }];
    mocks.status.mockResolvedValue(state);
    mocks.configure.mockRejectedValue(new Error("本地隐私推理运行时未安装或不兼容"));
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('input[type="checkbox"]')).not.toBeNull());
    click(mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!);
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="alert"]')?.textContent).toContain("运行时未安装"));
    expect(mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false);
    expect(mocks.configure).toHaveBeenCalledWith({ enabled: true, precision: state.config.precision, revision: state.config.revision });
  });

  it("selects an installed model without silently enabling protection", async () => {
    const state = baseline();
    const installed = { precision: "fp32" as const, revision: "b".repeat(40), version: "v0.1.3", files: {} };
    state.installed = [installed];
    mocks.status.mockResolvedValue(state);
    mocks.configure.mockResolvedValue({ ...state, config: { ...state.config, ...installed } });
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('[aria-label="使用的本地隐私模型"]')).not.toBeNull());
    const select = mounted.container.querySelector<HTMLSelectElement>('[aria-label="使用的本地隐私模型"]')!;
    act(() => { select.value = `${installed.revision}:fp32`; select.dispatchEvent(new Event("change", { bubbles: true })); });
    await waitForRendered(() => expect(select.disabled).toBe(false));
    expect(mocks.configure).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, precision: "fp32", revision: installed.revision }));
  });
  it("checks model releases without switching the selected model", async () => {
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('input[type="checkbox"]')).not.toBeNull());
    const latest = { version: "v0.2.0", revision: "c".repeat(40) };
    mocks.checkUpdates.mockResolvedValue(latest);
    mocks.status.mockResolvedValue({ ...baseline(), latest });
    click(button("检查模型更新"));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("最新正式版：v0.2.0"));
    expect(mocks.checkUpdates).toHaveBeenCalledOnce();
    expect(mocks.configure).not.toHaveBeenCalled();
    expect(mocks.download).not.toHaveBeenCalled();
  });

});
