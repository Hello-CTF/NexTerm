import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWailsMock, type WailsMock } from "./wailsMock";

let runtime: WailsMock;

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
});

describe("Wails 原生对话框", () => {
  it("ask 保留标题/kind，并按按钮结果返回 boolean", async () => {
    runtime.Dialogs.Warning.mockResolvedValue("确定");
    const { ask } = await import("../ui/dialogs");

    await expect(ask("继续吗？", { title: "确认", kind: "warning" })).resolves.toBe(true);
    expect(runtime.Dialogs.Warning).toHaveBeenCalledWith({
      Title: "确认",
      Message: "继续吗？",
      Buttons: [
        { Label: "取消", IsCancel: true },
        { Label: "确定", IsDefault: true },
      ],
    });
  });

  it("confirm/message 使用原生问题和信息框", async () => {
    runtime.Dialogs.Question.mockResolvedValue("取消");
    runtime.Dialogs.Info.mockResolvedValue("确定");
    const { confirmDialog, messageBox } = await import("../ui/dialogs");

    await expect(confirmDialog("删除吗？")).resolves.toBe(false);
    await messageBox("已完成");
    expect(runtime.Dialogs.Info).toHaveBeenCalledWith({
      Message: "已完成",
      Buttons: [{ Label: "确定", IsDefault: true }],
    });
  });

  it("open 取消归一为 null，私钥 filters 保留所有文件", async () => {
    runtime.Dialogs.OpenFile.mockResolvedValueOnce("").mockResolvedValueOnce("/tmp/id_ed25519");
    const { pickLocalFile, pickKeyFile } = await import("../ui/dialogs");

    await expect(pickLocalFile()).resolves.toBeNull();
    await expect(pickKeyFile()).resolves.toBe("/tmp/id_ed25519");
    expect(runtime.Dialogs.OpenFile.mock.calls[0][0]).not.toHaveProperty("Filters");
    const options = runtime.Dialogs.OpenFile.mock.calls[1][0] as {
      AllowsMultipleSelection: boolean;
      Filters: { DisplayName: string; Pattern: string }[];
    };
    expect(options.AllowsMultipleSelection).toBe(false);
    expect(options.Filters[0].Pattern).toBe("*.pem;*.key;*.ppk;*.id_rsa;*.id_ed25519;*.openssh");
    expect(options.Filters[1]).toEqual({ DisplayName: "所有文件", Pattern: "*" });
  });

  it("save 传默认文件名并把取消归一为 null", async () => {
    runtime.Dialogs.SaveFile.mockResolvedValue("");
    const { pickSavePath } = await import("../ui/dialogs");

    await expect(pickSavePath("audit.log")).resolves.toBeNull();
    expect(runtime.Dialogs.SaveFile).toHaveBeenCalledWith({ Filename: "audit.log" });
  });

  it("应用内注册 handler 仍优先于原生弹框", async () => {
    const { ask, registerDialogHandlers } = await import("../ui/dialogs");
    const customAsk = vi.fn().mockResolvedValue(true);
    registerDialogHandlers({
      ask: customAsk,
      confirm: vi.fn(),
      message: vi.fn(),
      choose: vi.fn(),
    });

    await expect(ask("自定义")).resolves.toBe(true);
    expect(customAsk).toHaveBeenCalledWith("自定义", undefined);
    expect(runtime.Dialogs.Question).not.toHaveBeenCalled();
  });
});

describe("Wails 窗口", () => {
  it("最小化、最大化切换和关闭映射到当前窗口", async () => {
    const { minimiseWindow, toggleMaximiseWindow, closeWindow } = await import("../ipc/wails");

    await minimiseWindow();
    await toggleMaximiseWindow();
    await closeWindow();

    expect(runtime.Window.Minimise).toHaveBeenCalledTimes(1);
    expect(runtime.Window.ToggleMaximise).toHaveBeenCalledTimes(1);
    expect(runtime.Window.Close).toHaveBeenCalledTimes(1);
  });

  it("拖动区域使用 Wails CSS 契约", async () => {
    const { wailsDragRegionStyle } = await import("../app/platform");
    expect(wailsDragRegionStyle).toEqual({ "--wails-draggable": "drag" });
  });
});
