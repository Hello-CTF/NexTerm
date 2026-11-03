/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

describe("port discovery preference", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
  });

  it("defaults to off", async () => {
    const { getTerminalPrefs } = await import("../../app/preferences");
    expect(getTerminalPrefs().portDiscovery).toBe("off");
  });

  it("falls back to off for invalid stored values", async () => {
    localStorage.setItem("nexterm.terminalClose.v1", JSON.stringify({ portDiscovery: "yes" }));
    const { getTerminalPrefs } = await import("../../app/preferences");
    expect(getTerminalPrefs().portDiscovery).toBe("off");
  });

  it("stores and reloads on", async () => {
    const first = await import("../../app/preferences");
    first.setTerminalPortDiscovery("on");
    vi.resetModules();
    const second = await import("../../app/preferences");
    expect(second.getTerminalPrefs().portDiscovery).toBe("on");
  });
});
