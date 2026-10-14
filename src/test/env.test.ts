import { beforeEach, describe, expect, it, vi } from "vitest";

beforeEach(() => {
  vi.resetModules();
});

function windowWith(value: Record<string, unknown>, search = "") {
  return { ...value, location: { search } };
}

describe("desktop/web detection", () => {
  it("recognizes explicit desktop markers and the Wails global as desktop", async () => {
    vi.stubGlobal("window", windowWith({ __NEXTERM_TRANSPORT__: "desktop" }));
    let env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("desktop");

    vi.resetModules();
    vi.stubGlobal("window", windowWith({ wails: {} }));
    env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("desktop");
  });

  it("treats explicit web markers and plain browser fallback as web", async () => {
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
      windowWith({ __NEXTERM_TRANSPORT__: "desktop", wails: {} }),
    );
    env = await import("../ipc/env");
    expect(env.TRANSPORT).toBe("desktop");
  });

  it("persists clientId and consistently falls back to memory when storage is unavailable", async () => {
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
