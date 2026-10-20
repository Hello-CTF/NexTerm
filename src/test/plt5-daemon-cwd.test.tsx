/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const h = vi.hoisted(() => ({
  web: true,
  setText: vi.fn(),
  text: vi.fn(),
}));

vi.mock("../ipc/env", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/env")>()),
  get WEB() {
    return h.web;
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Clipboard: { SetText: h.setText, Text: h.text },
}));

import { Cwd, shortenCwd } from "../features/terminal/Cwd";
import { useUi } from "../app/store";

const mounted: MountedView[] = [];

beforeEach(() => {
  h.web = true;
  h.setText.mockReset().mockResolvedValue(undefined);
  h.text.mockReset().mockResolvedValue("/srv/www/app");
});

afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("shortenCwd", () => {
  it("keeps short paths intact", () => {
    expect(shortenCwd("/etc/nginx")).toBe("/etc/nginx");
  });

  it("keeps the last two segments of long paths", () => {
    expect(shortenCwd("/very/long/path/to/some/project")).toBe("…/some/project");
  });

  it("handles root", () => {
    expect(shortenCwd("/")).toBe("/");
  });
});

describe("Cwd", () => {
  it("renders nothing without a cwd", () => {
    const view = mount(createElement(Cwd, { cwd: null }));
    mounted.push(view);
    expect(view.container.querySelector("button")).toBeNull();
  });

  it("renders the reported cwd and copies the full path on click", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    const view = mount(createElement(Cwd, { cwd: "/srv/www/app" }));
    mounted.push(view);
    const button = view.container.querySelector("button");
    expect(button?.textContent).toBe("/srv/www/app");
    expect(button?.getAttribute("title")).toContain("/srv/www/app");
    click(button!);
    expect(writeText).toHaveBeenCalledWith("/srv/www/app");
    await act(async () => {
      await writeText.mock.results[0]?.value;
    });
  });

  it("pushes an error toast when the copy fails", async () => {
    const toast = vi.fn();
    const original = useUi.getState().pushToast;
    useUi.setState({ pushToast: toast });
    try {
      const writeText = vi.fn().mockRejectedValue(new Error("denied"));
      vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
      const view = mount(createElement(Cwd, { cwd: "/srv/www/app" }));
      mounted.push(view);
      click(view.container.querySelector("button")!);
      await flushUntil(() => toast.mock.calls.length > 0);
      expect(toast).toHaveBeenCalledWith("error", expect.stringContaining("复制工作目录失败"));
    } finally {
      act(() => useUi.setState({ pushToast: original }));
    }
  });

  it("falls back to the wails clipboard on desktop when the API is missing", async () => {
    h.web = false;
    const toast = vi.fn();
    const original = useUi.getState().pushToast;
    useUi.setState({ pushToast: toast });
    try {
      vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
      const view = mount(createElement(Cwd, { cwd: "/srv/www/app" }));
      mounted.push(view);
      click(view.container.querySelector("button")!);
      await flushUntil(() => toast.mock.calls.length > 0);
      expect(h.setText).toHaveBeenCalledWith("/srv/www/app");
      expect(toast).toHaveBeenCalledWith("success", "已复制工作目录");
    } finally {
      act(() => useUi.setState({ pushToast: original }));
    }
  });
});
