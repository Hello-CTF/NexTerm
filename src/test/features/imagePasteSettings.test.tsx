/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";

const mocks = vi.hoisted(() => ({
  flags: { web: false },
  settingsGet: vi.fn(),
  settingsSet: vi.fn(),
  saveImage: vi.fn(),
  fetchImageService: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  filesApi: {
    settingsGet: mocks.settingsGet,
    settingsSet: mocks.settingsSet,
    saveImage: mocks.saveImage,
  },
}));

vi.mock("../../ipc/webFiles", () => ({
  fetchImageService: mocks.fetchImageService,
}));

vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return {
    ...actual,
    get WEB() {
      return mocks.flags.web;
    },
  };
});

import { FilesCard } from "../../features/settings/FilesCard";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const HEALTH_CONFIGURED = {
  publicBaseURLConfigured: true,
  maxBytes: 20 << 20,
  ownerQuotaBytes: 200 << 20,
  ttlSeconds: 86400,
};
const HEALTH_UNSET = { ...HEALTH_CONFIGURED, publicBaseURLConfigured: false };

describe("FilesCard 默认文件访问基础 URL", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    mocks.flags.web = false;
    mocks.settingsGet.mockReset().mockResolvedValue({ publicBaseURL: "" });
    mocks.settingsSet.mockReset().mockImplementation(async (value: string) => ({
      publicBaseURL: value,
    }));
    mocks.saveImage.mockReset();
    mocks.fetchImageService.mockReset().mockResolvedValue(null);
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

  it("未设置覆盖值且服务端无默认时展示同源相对链接语义", async () => {
    mocks.flags.web = true;
    mocks.fetchImageService.mockResolvedValue(HEALTH_UNSET);
    mounted = mount(createElement(FilesCard));
    await flush();

    const c = mounted.container;
    expect(c.textContent).toContain("同源相对链接");
    expect(input(c).value).toBe("");
    expect(c.textContent).toContain("/files/image/…");
  });

  it("未设置覆盖值但服务端 CLI/env 默认生效时不谎称同源相对", async () => {
    mocks.flags.web = true;
    mocks.fetchImageService.mockResolvedValue(HEALTH_CONFIGURED);
    mounted = mount(createElement(FilesCard));
    await flush();

    const c = mounted.container;
    expect(c.textContent).toContain("服务端默认已配置");
    expect(c.textContent).toContain("--public-base-url");
    expect(c.textContent).not.toContain("同源相对链接");
  });

  it("覆盖值清除后回到服务端默认配置提示", async () => {
    mocks.flags.web = true;
    mocks.fetchImageService.mockResolvedValue(HEALTH_CONFIGURED);
    mocks.settingsGet.mockResolvedValue({ publicBaseURL: "https://example.com/nexterm" });
    mounted = mount(createElement(FilesCard));
    await flush();

    setInputValue(input(mounted.container), "");
    click([...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "保存")!);
    await flush();

    expect(mocks.settingsSet).toHaveBeenCalledWith("");
    expect(mounted.container.textContent).toContain("服务端默认已配置");
    expect(mounted.container.textContent).not.toContain("同源相对链接");
  });

  it("桌面端不探测服务端, 展示无覆盖值的中性提示", async () => {
    mocks.flags.web = false;
    mounted = mount(createElement(FilesCard));
    await flush();

    const c = mounted.container;
    expect(mocks.fetchImageService).not.toHaveBeenCalled();
    expect(c.textContent).toContain("未设置覆盖值");
    expect(c.textContent).toContain("以其为准");
  });

  it("已设置时回显持久化值", async () => {
    mocks.settingsGet.mockResolvedValue({ publicBaseURL: "https://example.com/nexterm" });
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
    expect(mocks.settingsSet).not.toHaveBeenCalled();
  });

  it("保存成功时按规范化值写入并更新展示", async () => {
    mounted = mount(createElement(FilesCard));
    await flush();

    setInputValue(input(mounted.container), "https://example.com/nexterm/");
    click([...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "保存")!);
    await flush();

    expect(mocks.settingsSet).toHaveBeenCalledWith("https://example.com/nexterm");
    expect(input(mounted.container).value).toBe("https://example.com/nexterm");
  });

  it("读取失败时给出可重试的错误态", async () => {
    mocks.settingsGet.mockRejectedValue(new Error("boom"));
    mounted = mount(createElement(FilesCard));
    await flush();

    expect(mounted.container.textContent).toContain("读取文件链接设置失败");
    mocks.settingsGet.mockResolvedValue({ publicBaseURL: "" });
    click([...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "重试")!);
    await flush();

    expect(mocks.settingsGet).toHaveBeenCalledTimes(2);
    expect(mounted.container.textContent).toContain("未设置覆盖值");
  });
});
