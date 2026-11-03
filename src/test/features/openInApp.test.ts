/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const env = vi.hoisted(() => ({ web: false, mobile: false, openExternal: vi.fn() }));

vi.mock("../../ipc/env", () => ({
  get WEB() {
    return env.web;
  },
}));
vi.mock("../../app/shell/shellMode", () => ({ isMobileShell: () => env.mobile }));
vi.mock("../../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/wails")>()),
  openExternalURL: env.openExternal,
}));

import { useUi } from "../../app/store";
import { openUrlInApp } from "../../features/browser/openInApp";

const addTab = vi.fn();
const pushToast = vi.fn();

beforeEach(() => {
  env.web = false;
  env.mobile = false;
  env.openExternal.mockReset().mockResolvedValue(undefined);
  addTab.mockReset();
  pushToast.mockReset();
  vi.spyOn(useUi, "getState").mockReturnValue({ addTab, pushToast } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("openUrlInApp", () => {
  it("opens an embeddable url as a browser tab titled by its host", () => {
    openUrlInApp("https://example.com:8443/docs?x=1");
    expect(pushToast).not.toHaveBeenCalled();
    expect(addTab).toHaveBeenCalledTimes(1);
    expect(addTab.mock.calls[0][0]).toMatchObject({
      kind: "browser",
      title: "example.com:8443",
      url: "https://example.com:8443/docs?x=1",
      closable: true,
    });
    expect(addTab.mock.calls[0][0].id).toMatch(/^browser/);
  });

  it("refuses a non-http url with an error toast and opens nothing", () => {
    openUrlInApp("ftp://example.com/file");
    expect(addTab).not.toHaveBeenCalled();
    expect(env.openExternal).not.toHaveBeenCalled();
    expect(pushToast).toHaveBeenCalledWith("error", expect.stringContaining("无法在应用内打开"));
  });

  it("hands off to the system browser on mobile", () => {
    env.mobile = true;
    openUrlInApp("https://example.com/");
    expect(addTab).not.toHaveBeenCalled();
    expect(pushToast).toHaveBeenCalledWith("info", expect.stringContaining("已改用系统浏览器"));
    expect(env.openExternal).toHaveBeenCalledWith("https://example.com/");
  });

  it("reports a failure to launch the system browser", async () => {
    env.mobile = true;
    env.openExternal.mockRejectedValue(new Error("no handler"));
    openUrlInApp("https://example.com/");
    await vi.waitFor(() => expect(pushToast).toHaveBeenCalledWith("error", expect.stringContaining("无法打开链接")));
    expect(pushToast).toHaveBeenLastCalledWith("error", expect.stringContaining("no handler"));
  });

  it("opens an http url in a new window in browser mode instead of embedding it", () => {
    env.web = true;
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    openUrlInApp("http://example.com/");
    expect(open).toHaveBeenCalledWith("http://example.com/", "_blank", "noopener,noreferrer");
    expect(env.openExternal).not.toHaveBeenCalled();
    expect(addTab).not.toHaveBeenCalled();
    expect(pushToast).toHaveBeenCalledWith("info", expect.any(String));
  });
});
