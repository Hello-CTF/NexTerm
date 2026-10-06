/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";

const filesApiMock = vi.hoisted(() => ({
  settingsGet: vi.fn(),
  settingsSet: vi.fn(),
  saveImage: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  filesApi: filesApiMock,
}));

import { FilesCard } from "../../features/settings/FilesCard";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

describe("FilesCard 默认文件访问基础 URL", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    filesApiMock.settingsGet.mockReset().mockResolvedValue({ publicBaseURL: "" });
    filesApiMock.settingsSet.mockReset().mockImplementation(async (value: string) => ({
      publicBaseURL: value,
    }));
    filesApiMock.saveImage.mockReset();
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  function input(container: ParentNode): HTMLInputElement {
    const el = container.querySelector<HTMLInputElement>("#files-public-base-url");
    if (!el) throw new Error("public base URL input not found");
    return el;
  }

  it("未设置时展示同源相对链接语义", async () => {
    mounted = mount(createElement(FilesCard));
    await flush();

    const c = mounted.container;
    expect(c.textContent).toContain("同源相对链接");
    expect(input(c).value).toBe("");
    expect(input(c).placeholder).toContain("/files/image/…");
  });

  it("已设置时回显持久化值", async () => {
    filesApiMock.settingsGet.mockResolvedValue({ publicBaseURL: "https://example.com/nexterm" });
    mounted = mount(createElement(FilesCard));
    await flush();

    const c = mounted.container;
    expect(input(c).value).toBe("https://example.com/nexterm");
    expect(c.textContent).toContain("已设置基础 URL");
    expect(c.textContent).toContain("https://example.com/nexterm/files/image/…");
  });

  it("与后端相同的校验失败时内联报错且不发起保存", async () => {
    mounted = mount(createElement(FilesCard));
    await flush();

    setInputValue(input(mounted.container), "https://user:pass@example.com");
    click([...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "保存")!);
    await flush();

    expect(mounted.container.textContent).toContain("不允许包含用户名或密码");
    expect(filesApiMock.settingsSet).not.toHaveBeenCalled();
  });

  it("保存成功时按规范化值写入并更新展示", async () => {
    mounted = mount(createElement(FilesCard));
    await flush();

    setInputValue(input(mounted.container), "https://example.com/nexterm/");
    click([...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "保存")!);
    await flush();

    expect(filesApiMock.settingsSet).toHaveBeenCalledWith("https://example.com/nexterm");
    expect(input(mounted.container).value).toBe("https://example.com/nexterm");
  });

  it("读取失败时给出可重试的错误态", async () => {
    filesApiMock.settingsGet.mockRejectedValue(new Error("boom"));
    mounted = mount(createElement(FilesCard));
    await flush();

    expect(mounted.container.textContent).toContain("读取文件链接设置失败");
    filesApiMock.settingsGet.mockResolvedValue({ publicBaseURL: "" });
    click([...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "重试")!);
    await flush();

    expect(filesApiMock.settingsGet).toHaveBeenCalledTimes(2);
    expect(mounted.container.textContent).toContain("同源相对链接");
  });
});
