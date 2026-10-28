import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushUntil } from "./reactTestUtils";

interface FakeTerm {
  loadAddon: ReturnType<typeof vi.fn>;
}

function fakeTerm(): FakeTerm {
  return { loadAddon: vi.fn() };
}

type Addons = typeof import("../../features/terminal/xtermAddons");

async function loadModule(mocks: {
  webgl?: () => Record<string, unknown>;
  search?: () => Record<string, unknown>;
  links?: () => Record<string, unknown>;
}): Promise<Addons> {
  vi.resetModules();
  vi.doMock("@xterm/addon-webgl", mocks.webgl ?? (() => ({ WebglAddon: class {} })));
  vi.doMock(
    "@xterm/addon-search",
    mocks.search ??
      (() => ({
        SearchAddon: class {
          findNext = vi.fn();
          findPrevious = vi.fn();
        },
      })),
  );
  vi.doMock("@xterm/addon-web-links", mocks.links ?? (() => ({ WebLinksAddon: class {} })));
  return import("../../features/terminal/xtermAddons");
}

beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", (cb: () => void) => setTimeout(cb, 0) as unknown as number);
  vi.stubGlobal("cancelAnimationFrame", (id: number) => clearTimeout(id));
  vi.stubGlobal("window", globalThis);
  (globalThis as { requestIdleCallback?: unknown }).requestIdleCallback = (cb: () => void) =>
    setTimeout(cb, 0) as unknown as number;
  (globalThis as { cancelIdleCallback?: unknown }).cancelIdleCallback = (id: number) => clearTimeout(id);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.doUnmock("@xterm/addon-webgl");
  vi.doUnmock("@xterm/addon-search");
  vi.doUnmock("@xterm/addon-web-links");
});

describe("scheduleWebglRenderer", () => {
  it("attaches the WebGL addon only after first paint and idle", async () => {
    const { scheduleWebglRenderer } = await loadModule({});
    const term = fakeTerm();
    scheduleWebglRenderer(term as never, () => false, { mode: "on" });
    expect(term.loadAddon).not.toHaveBeenCalled();
    await flushUntil(() => term.loadAddon.mock.calls.length === 1);
  });

  it("keeps the DOM renderer when the chunk fails to load", async () => {
    const { scheduleWebglRenderer } = await loadModule({
      webgl: () => {
        throw new Error("chunk load failed");
      },
    });
    const term = fakeTerm();
    scheduleWebglRenderer(term as never, () => false, { mode: "on" });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(term.loadAddon).not.toHaveBeenCalled();
  });

  it("keeps the DOM renderer when the addon cannot create a GL context", async () => {
    const { scheduleWebglRenderer } = await loadModule({});
    const term = fakeTerm();
    term.loadAddon.mockImplementation(() => {
      throw new Error("no webgl");
    });
    scheduleWebglRenderer(term as never, () => false, { mode: "on" });
    await flushUntil(() => term.loadAddon.mock.calls.length === 1);
  });

  it("skips WebGL in auto mode without WebGL2 and follows visibility and mode changes", async () => {
    const dispose = vi.fn();
    const clearTextureAtlas = vi.fn();
    const { scheduleWebglRenderer } = await loadModule({
      webgl: () => ({ WebglAddon: class { dispose = dispose; clearTextureAtlas = clearTextureAtlas; } }),
    });
    const term = { ...fakeTerm(), refresh: vi.fn(), rows: 24 };
    const renderer = scheduleWebglRenderer(term as never, () => false);
    await new Promise((resolve) => setTimeout(resolve, 30));
    // jsdom has no WebGL2, so auto mode stays on the DOM renderer.
    expect(term.loadAddon).not.toHaveBeenCalled();
    renderer.setMode("on");
    await flushUntil(() => term.loadAddon.mock.calls.length === 1);
    renderer.setVisible(false);
    renderer.setMode("off");
    expect(dispose).toHaveBeenCalledTimes(1);
    renderer.setVisible(true);
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(term.loadAddon).toHaveBeenCalledTimes(1);
    expect(clearTextureAtlas).not.toHaveBeenCalled();
    renderer.cancel();
  });

  it("does nothing after cancel or disposal", async () => {
    const { scheduleWebglRenderer } = await loadModule({});
    const cancelled = fakeTerm();
    scheduleWebglRenderer(cancelled as never, () => false, { mode: "on" }).cancel();
    const disposed = fakeTerm();
    scheduleWebglRenderer(disposed as never, () => true, { mode: "on" });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(cancelled.loadAddon).not.toHaveBeenCalled();
    expect(disposed.loadAddon).not.toHaveBeenCalled();
  });
});

describe("attachLazyAddons", () => {
  it("loads search and links, and runs a search issued before the addon arrived", async () => {
    const { attachLazyAddons } = await loadModule({});
    const term = fakeTerm();
    const api = attachLazyAddons(term as never, { handler: () => undefined }, () => false);
    api.findNext("needle");
    await flushUntil(() => term.loadAddon.mock.calls.length === 2);
    const search = term.loadAddon.mock.calls
      .map(([addon]) => addon as { findNext?: ReturnType<typeof vi.fn> })
      .find((addon) => addon.findNext);
    await flushUntil(() => (search?.findNext?.mock.calls.length ?? 0) === 1);
    expect(search?.findNext).toHaveBeenCalledWith("needle");
  });

  it("leaves the terminal usable when the addons fail to load", async () => {
    const fail = () => {
      throw new Error("chunk load failed");
    };
    const { attachLazyAddons } = await loadModule({ search: fail, links: fail });
    const term = fakeTerm();
    const api = attachLazyAddons(term as never, { handler: () => undefined }, () => false);
    expect(() => api.findNext("x")).not.toThrow();
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(term.loadAddon).not.toHaveBeenCalled();
  });
});
