/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";
import type { PrivacyStatus } from "../../ipc/privacy";

const mocks = vi.hoisted(() => ({ status: vi.fn(), configure: vi.fn(), download: vi.fn(), checkUpdates: vi.fn(), cancelDownload: vi.fn(), remove: vi.fn(), confirm: vi.fn() }));
vi.mock("../../ipc/commands", () => ({
  call: (command: string, args: Record<string, unknown>) => {
    switch (command) {
      case "ai_privacy_status": return mocks.status();
      case "ai_privacy_configure": return mocks.configure(args);
      case "ai_privacy_download": return mocks.download(args.precision);
      case "ai_privacy_check_updates": return mocks.checkUpdates();
      case "ai_privacy_cancel_download": return mocks.cancelDownload();
      case "ai_privacy_remove": return mocks.remove(args);
      default: throw new Error(`Unexpected command: ${command}`);
    }
  },
}));
vi.mock("../../ipc/env", () => ({ WEB: true }));
vi.mock("../../ui/dialogs", () => ({ confirmDialog: mocks.confirm }));
vi.mock("../../features/auth/store", () => ({ useAuth: (select: (value: unknown) => unknown) => select({ user: { id: "alice" } }) }));
import { PrivacyCard } from "../../features/settings/PrivacyCard";

const baseline = (): PrivacyStatus => ({
  config: { enabled: false, precision: "int8", revision: "", version: "" },
  installed: [],
  runtime: { supported: true },
  diskUsage: 0,
  canManage: true,
  latest: { version: "v0.1.3", revision: "c".repeat(40) },
  latestSizes: { int8: 300 * 1024 * 1024, fp32: 1100 * 1024 * 1024 },
  download: { running: false, precision: "int8", progress: { file: "", downloaded: 0, total: 0 } },
});
let mounted: MountedView | undefined;
beforeEach(() => { vi.resetAllMocks(); mocks.status.mockResolvedValue(baseline()); });
afterEach(() => { mounted?.unmount(); mounted = undefined; });
function button(label: string): HTMLButtonElement {
  const found = [...mounted!.container.querySelectorAll("button")].find((item) => item.textContent?.replace(/\s+/g, " ").trim() === label);
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
    click(button("下载 FP32（约 1100 MiB）"));
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
  it("fetches release sizes on first load and reports a newer release without switching models", async () => {
    const initial = baseline();
    delete initial.latest;
    delete initial.latestSizes;
    initial.installed = [{ version: "v0.1.2", revision: "b".repeat(40), precision: "int8", files: { "model.onnx": { size: 1024, sha256: "a".repeat(64) } } }];
    const latest = { version: "v0.2.0", revision: "c".repeat(40) };
    mocks.status.mockResolvedValueOnce(initial).mockResolvedValue({ ...initial, latest, latestSizes: { int8: 200 * 1024 * 1024 } });
    mocks.checkUpdates.mockResolvedValue(latest);
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("有新版本 v0.2.0，下载后可切换"));
    expect(mocks.checkUpdates).toHaveBeenCalledOnce();
    expect(button("下载 INT8（约 200 MiB）")).toBeTruthy();
    expect(mocks.configure).not.toHaveBeenCalled();
    expect(mocks.download).not.toHaveBeenCalled();
  });

  it("disables downloading and enabling on an unsupported device", async () => {
    mocks.status.mockResolvedValue({ ...baseline(), runtime: { supported: false, reason: "本地 AI 脱敏需要 macOS 13.4 或更新版本" } });
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("此设备无法运行本地脱敏：本地 AI 脱敏需要 macOS 13.4 或更新版本。"));
    expect(button("下载 INT8（约 300 MiB）").disabled).toBe(true);
    expect(mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.disabled).toBe(true);
    expect(mounted.container.textContent).toContain("需先下载并选择模型");
    expect(mocks.checkUpdates).not.toHaveBeenCalled();
  });

  it("warns that requests are blocked when the enabled selection is missing and keeps it visible", async () => {
    const state = baseline();
    state.config = { enabled: true, precision: "fp16", revision: "d".repeat(40), version: "v0.1.3" };
    state.selection = { available: false, error: "本地隐私模型文件缺失，请重新下载模型" };
    mocks.status.mockResolvedValue(state);
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="alert"]')?.textContent).toContain("已启用脱敏，但所选模型不可用：本地隐私模型文件缺失，请重新下载模型。在恢复或关闭前，所有 AI 请求都会被阻止。"));
    const select = mounted.container.querySelector<HTMLSelectElement>('[aria-label="使用的本地隐私模型"]')!;
    expect(select.selectedOptions[0]?.textContent).toContain("（文件缺失）");
    const toggle = mounted.container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(toggle.disabled).toBe(false);
    mocks.configure.mockResolvedValue({ ...state, config: { ...state.config, enabled: false } });
    click(toggle);
    await waitForRendered(() => expect(mocks.configure).toHaveBeenCalledWith(expect.objectContaining({ enabled: false })));
    await waitForRendered(() => expect(toggle.checked).toBe(false));
    expect(mounted.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("shows download failures with detail and cancellation as status", async () => {
    mocks.status.mockResolvedValue({ ...baseline(), download: { ...baseline().download, error: "磁盘空间不足，无法保存模型，请清理空间后重试", detail: "write model.onnx: no space left on device" } });
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="alert"]')?.textContent).toContain("磁盘空间不足"));
    expect(mounted.container.textContent).toContain("no space left on device");
    mounted.unmount();
    mocks.status.mockResolvedValue({ ...baseline(), download: { ...baseline().download, error: "下载已取消", canceled: true } });
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.querySelector('[role="status"]')?.textContent).toBe("下载已取消"));
    expect(mounted.container.querySelector('[role="alert"]')).toBeNull();
  });

  it("offers the completed download and confirms before deleting an installed model", async () => {
    const installed = { version: "v0.1.3", revision: "c".repeat(40), precision: "fp32" as const, files: { "model.onnx": { size: 2 * 1024 * 1024, sha256: "a".repeat(64) } } };
    const state = { ...baseline(), installed: [installed], diskUsage: 2 * 1024 * 1024, download: { ...baseline().download, precision: "fp32" as const, completed: { version: "v0.1.3", revision: installed.revision } } };
    mocks.status.mockResolvedValue(state);
    mounted = mount(createElement(PrivacyCard));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("下载完成：v0.1.3 FP32。"));
    expect(mounted.container.textContent).toContain("共占用 2.0 MiB");
    mocks.configure.mockResolvedValue({ ...state, config: { ...state.config, ...installed, enabled: false }, download: baseline().download });
    click(button("使用此模型"));
    await waitForRendered(() => expect(mocks.configure).toHaveBeenCalledWith({ enabled: false, precision: "fp32", revision: installed.revision }));
    await waitForRendered(() => expect(button("删除").disabled).toBe(false));
    mocks.confirm.mockResolvedValue(false);
    click(button("删除"));
    await waitForRendered(() => expect(mocks.confirm).toHaveBeenCalledWith("删除模型 v0.1.3 FP32（2.0 MiB）？"));
    expect(mocks.remove).not.toHaveBeenCalled();
    mocks.confirm.mockResolvedValue(true);
    mocks.remove.mockResolvedValue({ ...baseline() });
    click(button("删除"));
    await waitForRendered(() => expect(mocks.remove).toHaveBeenCalledWith({ revision: installed.revision, precision: "fp32" }));
    await waitForRendered(() => expect(mounted!.container.textContent).not.toContain("已下载的模型"));
  });

});
