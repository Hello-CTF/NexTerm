/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  linkGet: vi.fn(),
  openExternalURL: vi.fn(),
  copyText: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({ syncApi: { linkGet: mocks.linkGet } }));
vi.mock("../../ipc/wails", () => ({ openExternalURL: mocks.openExternalURL }));
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText }));

import { ServerSecurityLink } from "../../features/settings/ServerSecurityLink";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.linkGet.mockResolvedValue({ url: "https://example.invalid/nexterm", username: "alice" });
  mocks.openExternalURL.mockResolvedValue(undefined);
  mocks.copyText.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("desktop server security settings link", () => {
  it("opens the linked server's account settings section", async () => {
    mounted = mount(createElement(ServerSecurityLink));
    clickButton(mounted.container, "打开服务端安全设置");
    await flushUntil(() => mocks.openExternalURL.mock.calls.length === 1);
    expect(mocks.openExternalURL).toHaveBeenCalledWith("https://example.invalid/nexterm#settings-account");
  });

  it("shows and copies the exact destination when the browser cannot open", async () => {
    mocks.openExternalURL.mockRejectedValue(new Error("browser unavailable"));
    mounted = mount(createElement(ServerSecurityLink));
    clickButton(mounted.container, "打开服务端安全设置");
    await flushUntil(() => mounted!.container.textContent?.includes("浏览器打开失败") ?? false);
    const input = mounted.container.querySelector<HTMLInputElement>('input[aria-label="服务端安全设置地址"]');
    expect(input?.value).toBe("https://example.invalid/nexterm#settings-account");
    clickButton(mounted.container, "复制地址");
    await flushUntil(() => mocks.copyText.mock.calls.length === 1);
    expect(mocks.copyText).toHaveBeenCalledWith(input?.value);
  });

  it("rejects a saved URL containing embedded credentials", async () => {
    mocks.linkGet.mockResolvedValue({ url: "https://alice:secret@example.invalid", username: "alice" });
    mounted = mount(createElement(ServerSecurityLink));
    clickButton(mounted.container, "打开服务端安全设置");
    await flushUntil(() => mounted!.container.textContent?.includes("服务端地址无效") ?? false);
    expect(mocks.openExternalURL).not.toHaveBeenCalled();
  });
});
