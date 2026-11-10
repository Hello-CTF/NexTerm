import { describe, expect, it } from "vitest";
import type { Asset } from "../../ipc/commands";
import {
  changeSourceKind,
  defaultSourceDraft,
  isWindowsAsset,
  SOURCE_KIND_LABELS,
  sourceKindOptions,
} from "../../features/logs/logSources";

const asset = (patch: Partial<Asset>): Asset => ({ id: "a", name: "a", kind: "ssh", ...patch }) as Asset;

describe("log source kinds", () => {
  it("recognizes Windows assets from the kind or a PowerShell shell option", () => {
    expect(isWindowsAsset(asset({ kind: "winrm" }))).toBe(true);
    expect(isWindowsAsset(asset({ options: { shell: "powershell.exe" } }))).toBe(true);
    expect(isWindowsAsset(asset({ options: { shell: "C:\\Program Files\\PowerShell\\7\\pwsh.exe" } }))).toBe(true);
    expect(isWindowsAsset(asset({ options: { shell: "/bin/bash" } }))).toBe(false);
    expect(isWindowsAsset(asset({}))).toBe(false);
    expect(isWindowsAsset(undefined)).toBe(false);
  });

  it("leads with the event log for Windows hosts and keeps it selectable elsewhere", () => {
    expect(sourceKindOptions(true).map((option) => option.value)).toEqual(["winevent", "file", "docker"]);
    expect(sourceKindOptions(false).map((option) => option.value)).toEqual(["file", "journal", "docker", "winevent"]);
    expect(SOURCE_KIND_LABELS.winevent).toBe("Windows 事件日志");
  });

  it("defaults Windows hosts to the System channel and fills it when switching to the event log", () => {
    expect(defaultSourceDraft(true)).toEqual({ kind: "winevent", value: "System" });
    expect(defaultSourceDraft(false)).toEqual({ kind: "file", value: "" });
    expect(changeSourceKind({ kind: "file", value: "" }, "winevent")).toEqual({ kind: "winevent", value: "System" });
    expect(changeSourceKind({ kind: "file", value: "/var/log/a" }, "winevent")).toEqual({ kind: "winevent", value: "/var/log/a" });
  });
});
