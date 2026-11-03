/** @vitest-environment jsdom */
import { afterEach, describe, expect, it } from "vitest";
import { applyBackendPlatform, isMac, isWindows, localSessionIsMac, localSessionIsWindows, setBackendOS, setMacPlatform, setWindowsPlatform } from "../../app/platform";

afterEach(() => {
  setBackendOS(null);
  setMacPlatform(false);
  setWindowsPlatform(false);
});

describe("applyBackendPlatform", () => {
  it("keeps the browser's own detection when the backend is a server (web transport)", () => {
    setWindowsPlatform(true);
    applyBackendPlatform("linux", false);
    expect(isWindows()).toBe(true);
    setWindowsPlatform(false);
    setMacPlatform(true);
    applyBackendPlatform("windows", false);
    expect(isMac()).toBe(true);
    expect(isWindows()).toBe(false);
  });

  it("takes the backend OS on the desktop transport", () => {
    applyBackendPlatform("windows", true);
    expect(isWindows()).toBe(true);
    expect(isMac()).toBe(false);
    applyBackendPlatform("darwin", true);
    expect(isMac()).toBe(true);
    expect(isWindows()).toBe(false);
  });

  it("ignores an empty or non-string answer", () => {
    setWindowsPlatform(true);
    applyBackendPlatform("", true);
    applyBackendPlatform(undefined, true);
    expect(isWindows()).toBe(true);
  });

  it("judges local sessions by the server OS in the browser while keyboard detection stays on the browser", () => {
    setMacPlatform(true);
    applyBackendPlatform("windows", false);
    expect(isMac()).toBe(true);
    expect(isWindows()).toBe(false);
    expect(localSessionIsWindows()).toBe(true);
    expect(localSessionIsMac()).toBe(false);
    applyBackendPlatform("linux", false);
    expect(localSessionIsWindows()).toBe(false);
    expect(localSessionIsMac()).toBe(false);
  });

  it("matches isWindows and isMac on the desktop transport", () => {
    applyBackendPlatform("darwin", true);
    expect(localSessionIsMac()).toBe(isMac());
    expect(localSessionIsWindows()).toBe(isWindows());
  });
});
