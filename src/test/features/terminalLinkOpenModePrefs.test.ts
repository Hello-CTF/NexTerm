/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

describe("terminal link open location preference", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
  });

  it("defaults to system", async () => {
    const { getTerminalPrefs } = await import("../../app/preferences");
    expect(getTerminalPrefs().linkOpenMode).toBe("system");
  });

  it("falls back to the default for invalid stored values", async () => {
    localStorage.setItem("nexterm.terminalClose.v1", JSON.stringify({ linkOpenMode: "popup" }));
    const { getTerminalPrefs } = await import("../../app/preferences");
    expect(getTerminalPrefs().linkOpenMode).toBe("system");
  });

  it("stores and reloads a valid choice", async () => {
    const first = await import("../../app/preferences");
    first.setTerminalLinkOpenMode("app");
    expect(first.getTerminalPrefs().linkOpenMode).toBe("app");
    vi.resetModules();
    const second = await import("../../app/preferences");
    expect(second.getTerminalPrefs().linkOpenMode).toBe("app");
  });
});
