import { describe, expect, it } from "vitest";
import {
  dimensionsForControllerClaim,
  resolveWinrmMode,
  shouldFitTerminal,
  shouldSendTerminalResize,
} from "../../features/terminal/terminalPolicy";

describe("terminal mode and dimensions", () => {
  it("derives WinRM from session kind while preserving explicit overrides", () => {
    expect(resolveWinrmMode(undefined, "winrm")).toBe(true);
    expect(resolveWinrmMode(undefined, "ssh")).toBe(false);
    expect(resolveWinrmMode(false, "winrm")).toBe(false);
    expect(resolveWinrmMode(true, "ssh")).toBe(true);
  });

  it("never fits hidden containers or observer-side views", () => {
    expect(shouldFitTerminal(800, 600, true)).toBe(true);
    expect(shouldFitTerminal(0, 600, true)).toBe(false);
    expect(shouldFitTerminal(800, 0, true)).toBe(false);
    expect(shouldFitTerminal(800, 600, false)).toBe(false);
  });

  it("suppresses PTY resize while applying authoritative attached dimensions", () => {
    expect(shouldSendTerminalResize(true, false, true)).toBe(true);
    expect(shouldSendTerminalResize(false, false, true)).toBe(false);
    expect(shouldSendTerminalResize(true, true, true)).toBe(false);
    expect(shouldSendTerminalResize(true, false, false)).toBe(false);
  });

  it("fits the new controller viewport before reading claim dimensions", () => {
    const calls: string[] = [];
    const dimensions = dimensionsForControllerClaim({
      fit: () => calls.push("fit"),
      dimensions: () => {
        calls.push("dimensions");
        return { cols: 120, rows: 40 };
      },
    });
    expect(dimensions).toEqual({ cols: 120, rows: 40 });
    expect(calls).toEqual(["fit", "dimensions"]);
    expect(dimensionsForControllerClaim(null)).toBeNull();
  });
});
