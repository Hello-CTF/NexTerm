// @vitest-environment jsdom
import { act, lazy, createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ErrorBoundary } from "../../ui/ErrorBoundary";
import { Lazy, isChunkLoadError } from "../../ui/LazyBoundary";
import { shouldPrefetch } from "../../app/lazyPanels";
import { click, flushUntil, mount, type MountedView } from "../features/reactTestUtils";

let mounted: MountedView | null = null;

afterEach(() => {
  mounted?.unmount();
  mounted = null;
});

describe("isChunkLoadError", () => {
  it("recognises dynamic import failures from the major browsers", () => {
    expect(isChunkLoadError(new TypeError("Failed to fetch dynamically imported module: https://x/a.js"))).toBe(true);
    expect(isChunkLoadError(new TypeError("error loading dynamically imported module"))).toBe(true);
    expect(isChunkLoadError(new TypeError("Importing a module script failed."))).toBe(true);
    expect(isChunkLoadError(new Error("render crashed"))).toBe(false);
  });
});

describe("Lazy", () => {
  it("renders the loaded component", async () => {
    const Panel = lazy(() => Promise.resolve({ default: () => createElement("p", null, "已加载") }));
    mounted = mount(createElement(Lazy, null, createElement(Panel)));
    await flushUntil(() => mounted!.container.textContent === "已加载");
  });

  it("offers a reload button when the chunk cannot be downloaded", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    const Broken = lazy(() => Promise.reject(new TypeError("Failed to fetch dynamically imported module: /assets/x.js")));
    mounted = mount(createElement(Lazy, null, createElement(Broken)));
    await flushUntil(() => mounted!.container.querySelector('[role="alert"]') !== null);
    expect(mounted.container.textContent).toContain("界面模块加载失败");
    const button = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent === "重新加载");
    expect(button).toBeDefined();
    click(button!);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("leaves non-chunk errors to the surrounding error boundary", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const Crashing = lazy(() => Promise.resolve({ default: () => { throw new Error("render crashed"); } }));
    mounted = mount(createElement(ErrorBoundary, null, createElement(Lazy, null, createElement(Crashing))));
    await flushUntil(() => mounted!.container.textContent?.includes("面板发生错误") === true);
    expect(mounted.container.textContent).not.toContain("界面模块加载失败");
  });

  it("renders nothing for a failed background chunk when silent", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const Broken = lazy(() => Promise.reject(new TypeError("Failed to fetch dynamically imported module: /assets/y.js")));
    mounted = mount(createElement("div", null, createElement(Lazy, { silent: true, fallback: "none", children: createElement(Broken) })));
    await flushUntil(() => warn.mock.calls.length > 0);
    expect(mounted.container.querySelector('[role="alert"]')).toBeNull();
    expect(mounted.container.firstElementChild?.childElementCount).toBe(0);
  });

  it("clears a chunk failure when the slot switches to another component", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const Broken = lazy(() => Promise.reject(new TypeError("Failed to fetch dynamically imported module: /assets/z.js")));
    const Other = lazy(() => Promise.resolve({ default: () => createElement("p", null, "另一个面板") }));
    mounted = mount(createElement(Lazy, null, createElement(Broken)));
    await flushUntil(() => mounted!.container.querySelector('[role="alert"]') !== null);
    act(() => mounted!.root.render(createElement(Lazy, null, createElement(Other))));
    await flushUntil(() => mounted!.container.textContent === "另一个面板");
  });
});

describe("shouldPrefetch", () => {
  it("skips save-data and very slow connections", () => {
    expect(shouldPrefetch(undefined)).toBe(true);
    expect(shouldPrefetch({ effectiveType: "4g" })).toBe(true);
    expect(shouldPrefetch({ saveData: true, effectiveType: "4g" })).toBe(false);
    expect(shouldPrefetch({ effectiveType: "2g" })).toBe(false);
    expect(shouldPrefetch({ effectiveType: "slow-2g" })).toBe(false);
  });
});
