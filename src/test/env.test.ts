import { beforeEach, describe, expect, it, vi } from "vitest";

beforeEach(() => {
  vi.resetModules();
});

function windowWith(value: Record<string, unknown>, search = "") {
  return { ...value, location: { search } };
}

describe("desktop/web 检测", () => {
  it("显式 desktop 标记和 Wails 全局都识别为桌面", async () => {
    vi.stubGlobal("window", windowWith({ __NEXTERM_TRANSPORT__: "desktop" }));
    let env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("desktop");

    vi.resetModules();
    vi.stubGlobal("window", windowWith({ wails: {} }));
    env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("desktop");
  });

  it("Web 标记与普通浏览器兜底都是 web", async () => {
    vi.stubGlobal("window", windowWith({ __NEXTERM_TRANSPORT__: "web" }));
    let env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("web");
    expect(env.WEB).toBe(true);

    vi.resetModules();
    vi.stubGlobal("window", windowWith({}));
    env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("web");

    vi.resetModules();
    vi.stubGlobal(
      "window",
      windowWith({ __NEXTERM_TRANSPORT__: "desktop", wails: {} }, "?demo=1"),
    );
    env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("desktop");
  });

  it("clientId 持久化并在存储不可用时稳定退回内存", async () => {
    const values = new Map<string, string>();
    const localStorage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
    };
    vi.stubGlobal("window", windowWith({ localStorage }));
    let env = await import("../ipc/env");
    const stable = env.clientId();
    expect(env.clientId()).toBe(stable);

    vi.resetModules();
    env = await import("../ipc/env");
    expect(env.clientId()).toBe(stable);

    vi.resetModules();
    vi.stubGlobal(
      "window",
      windowWith({
        localStorage: {
          getItem: () => {
            throw new Error("blocked");
          },
          setItem: () => {
            throw new Error("blocked");
          },
        },
      }),
    );
    env = await import("../ipc/env");
    expect(env.clientId()).toBe(env.clientId());
  });
});
