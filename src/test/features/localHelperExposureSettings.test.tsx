/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  getExposure: vi.fn(),
  setExposure: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    localHelperApi: { getExposure: mocks.getExposure, setExposure: mocks.setExposure },
  });
});

import { LocalHelperExposureSettings } from "../../features/settings/LocalHelperExposureSettings";
import { useUi } from "../../app/store";
import { flush, mount } from "./reactTestUtils";

function checkbox(container: HTMLElement): HTMLInputElement | null {
  return container.querySelector<HTMLInputElement>('input[aria-label="向服务器账号开放本机终端"]');
}

beforeEach(() => {
  vi.resetAllMocks();
  useUi.setState({ toasts: [] });
});

describe("local terminal exposure setting", () => {
  it("renders nothing where exposure is unsupported", async () => {
    mocks.getExposure.mockResolvedValue({ enabled: false, state: "unavailable", reason: "此环境不能向服务器开放本机终端" });
    const view = mount(createElement(LocalHelperExposureSettings));
    try {
      await flush();
      expect(mocks.getExposure).toHaveBeenCalled();
      expect(checkbox(view.container)).toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("turns exposure on only after the user confirms", async () => {
    mocks.getExposure.mockResolvedValue({ enabled: false, state: "off" });
    mocks.setExposure.mockResolvedValue({ enabled: true, state: "connecting", deviceId: "desk-1" });
    mocks.ask.mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const view = mount(createElement(LocalHelperExposureSettings));
    try {
      await flush();
      expect(checkbox(view.container)).not.toBeNull();
      expect(checkbox(view.container)?.checked).toBe(false);

      await act(async () => checkbox(view.container)!.click());
      await flush();
      expect(mocks.setExposure).not.toHaveBeenCalled();
      // The confirmation states what is handed over and what turning it off does not undo.
      const confirmation = String(mocks.ask.mock.calls[0][0]);
      for (const part of ["任意命令", "管理员", "AI 助手", "批量执行", "仅表示分享功能不可用", "后台会话", "锁定凭据库不会停止开放", "上报"]) {
        expect(confirmation).toContain(part);
      }

      await act(async () => checkbox(view.container)!.click());
      await flush();
      expect(mocks.setExposure).toHaveBeenCalledWith(true);
      expect(checkbox(view.container)?.checked).toBe(true);
      expect(view.container.querySelector("[data-local-helper-exposure]")?.getAttribute("data-local-helper-exposure")).toBe("connecting");
    } finally {
      view.unmount();
    }
  });

  it("turns exposure off without asking and shows why it stopped", async () => {
    mocks.getExposure.mockResolvedValue({ enabled: true, state: "online", deviceId: "desk-1" });
    mocks.setExposure.mockResolvedValue({ enabled: false, state: "revoked", reason: "服务器已吊销此设备" });
    const view = mount(createElement(LocalHelperExposureSettings));
    try {
      await flush();
      expect(checkbox(view.container)?.checked).toBe(true);
      await act(async () => checkbox(view.container)!.click());
      await flush();
      expect(mocks.ask).not.toHaveBeenCalled();
      expect(mocks.setExposure).toHaveBeenCalledWith(false);
      expect(view.container.textContent).toContain("服务器已吊销此设备");
    } finally {
      view.unmount();
    }
  });

  it("reports a refused change and keeps the backend's status", async () => {
    mocks.getExposure.mockResolvedValue({ enabled: false, state: "off" });
    mocks.setExposure.mockRejectedValue(new Error("请先在同步设置中登录服务器账号"));
    mocks.ask.mockResolvedValue(true);
    const view = mount(createElement(LocalHelperExposureSettings));
    try {
      await flush();
      expect(checkbox(view.container)).not.toBeNull();
      await act(async () => checkbox(view.container)!.click());
      await flush();
      expect(checkbox(view.container)?.checked).toBe(false);
      expect(useUi.getState().toasts.some((toast) => toast.text.includes("请先在同步设置中登录服务器账号"))).toBe(true);
    } finally {
      view.unmount();
    }
  });
});
