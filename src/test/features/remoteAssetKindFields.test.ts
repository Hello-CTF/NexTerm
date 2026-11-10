import { isHostAssetKind } from "../../features/hosts/hostDetail";
import { describe, expect, it } from "vitest";
import {
  assetKindFields,
  hostToolsSupported,
  isRemoteDesktopKind,
  optionsForKindChange,
  readRemoteDesktopOptions,
  validateResolution,
  writeRemoteDesktopOptions,
} from "../../features/explorer/assetKindFields";

describe("assetKindFields", () => {
  it("gives rdp and vnc their default ports", () => {
    expect(assetKindFields("rdp").defaultPort).toBe(3389);
    expect(assetKindFields("vnc").defaultPort).toBe(5900);
    expect(assetKindFields("ssh").defaultPort).toBe(22);
    expect(assetKindFields("postgres").defaultPort).toBe(5432);
  });

  it("hides the SSH-only parts for remote desktops", () => {
    for (const kind of ["rdp", "vnc"]) {
      const fields = assetKindFields(kind);
      expect(fields.authMethod).toBe(false);
      expect(fields.sshAdvanced).toBe(false);
      expect(fields.password).toBe(true);
      expect(fields.via).toBe("ssh-or-device");
    }
    expect(assetKindFields("ssh").sshAdvanced).toBe(true);
    expect(assetKindFields("ssh").authMethod).toBe(true);
  });

  it("asks for a user name only where the protocol has one", () => {
    expect(assetKindFields("rdp").username).toBe(true);
    expect(assetKindFields("vnc").username).toBe(false);
  });

  it("keeps databases as they were: password only, through SSH only", () => {
    const fields = assetKindFields("mysql");
    expect(fields).toMatchObject({ authMethod: false, password: true, via: "ssh", remote: null });
  });

  it("falls back to the SSH shape for an unknown kind", () => {
    expect(assetKindFields("unknown").defaultPort).toBe(22);
    expect(isRemoteDesktopKind("rdp")).toBe(true);
    expect(isRemoteDesktopKind("ssh")).toBe(false);
  });
});

describe("remote desktop options", () => {
  it("reads defaults from an empty asset", () => {
    expect(readRemoteDesktopOptions("rdp", undefined)).toMatchObject({
      domain: "",
      promptPassword: false,
      clipboard: true,
      viewOnly: false,
      resolution: "follow",
    });
  });

  it("defaults clipboard sync on and Command-as-Ctrl by protocol, and stores only changes", () => {
    expect(readRemoteDesktopOptions("rdp", undefined)).toMatchObject({ clipboardAutoSync: true, commandAsCtrl: true });
    expect(readRemoteDesktopOptions("vnc", undefined)).toMatchObject({ clipboardAutoSync: true, commandAsCtrl: false });
    const rdp = writeRemoteDesktopOptions("rdp", {}, { ...readRemoteDesktopOptions("rdp", undefined), clipboardAutoSync: false, commandAsCtrl: false });
    expect(rdp).toEqual({ clipboardAutoSync: false, commandAsCtrl: false });
    expect(readRemoteDesktopOptions("rdp", rdp)).toMatchObject({ clipboardAutoSync: false, commandAsCtrl: false });
    expect(writeRemoteDesktopOptions("vnc", {}, { ...readRemoteDesktopOptions("vnc", undefined), commandAsCtrl: true })).toEqual({ commandAsCtrl: true });
    expect(writeRemoteDesktopOptions("rdp", { clipboardAutoSync: false, commandAsCtrl: false }, readRemoteDesktopOptions("rdp", undefined))).toEqual({});
  });

  it("drops the local shell and start directory when a local asset becomes another kind", () => {
    expect(optionsForKindChange("local", "ssh", { shell: "pwsh", cwd: "C:/work", icon: "server" })).toEqual({ icon: "server" });
    expect(optionsForKindChange("local", "local", { shell: "pwsh" })).toEqual({ shell: "pwsh" });
  });

  it("stores legacy TLS only for RDP and only when it is on", () => {
    expect(readRemoteDesktopOptions("rdp", undefined).legacyTLS).toBe(false);
    const on = writeRemoteDesktopOptions("rdp", {}, { ...readRemoteDesktopOptions("rdp", undefined), legacyTLS: true });
    expect(on).toEqual({ legacyTLS: true });
    expect(readRemoteDesktopOptions("rdp", on).legacyTLS).toBe(true);
    expect(writeRemoteDesktopOptions("rdp", on, readRemoteDesktopOptions("rdp", undefined))).toEqual({});
    expect(readRemoteDesktopOptions("vnc", { legacyTLS: true }).legacyTLS).toBe(false);
    expect(writeRemoteDesktopOptions("vnc", { legacyTLS: true }, readRemoteDesktopOptions("vnc", undefined))).toEqual({});
  });

  it("reads a fixed resolution only when both sides are set", () => {
    expect(readRemoteDesktopOptions("rdp", { width: 1280, height: 720 })).toMatchObject({ resolution: "fixed", width: 1280, height: 720 });
    expect(readRemoteDesktopOptions("rdp", { width: 1280 }).resolution).toBe("follow");
  });

  it("round-trips the form values and keeps options the form does not own", () => {
    const base = { viaAssetId: "jump-1", icon: "server", startupCommand: "keep-me" };
    const values = { ...readRemoteDesktopOptions("rdp", undefined), domain: " CORP ", promptPassword: true, resolution: "fixed" as const, width: 1600, height: 900 };
    const written = writeRemoteDesktopOptions("rdp", base, values);
    expect(written).toEqual({ viaAssetId: "jump-1", icon: "server", startupCommand: "keep-me", domain: "CORP", promptPassword: true, width: 1600, height: 900 });
    expect(readRemoteDesktopOptions("rdp", written)).toMatchObject({ domain: "CORP", promptPassword: true, resolution: "fixed", width: 1600, height: 900 });
  });

  it("drops the options of the other protocol and the ones turned off", () => {
    const written = writeRemoteDesktopOptions(
      "vnc",
      { domain: "OLD", width: 800, height: 600, viewOnly: true, clipboard: false, promptPassword: true },
      { ...readRemoteDesktopOptions("vnc", undefined), viewOnly: false },
    );
    expect(written).toEqual({});
  });

  it("writes read-only and the disabled clipboard for vnc", () => {
    const written = writeRemoteDesktopOptions("vnc", {}, { ...readRemoteDesktopOptions("vnc", undefined), viewOnly: true, clipboard: false });
    expect(written).toEqual({ viewOnly: true, clipboard: false });
  });

  it("validates a fixed resolution", () => {
    expect(validateResolution(1920, 1080)).toBeNull();
    expect(validateResolution(100, 100)).not.toBeNull();
    expect(validateResolution(9000, 1080)).not.toBeNull();
    expect(validateResolution(1920.5, 1080)).not.toBeNull();
  });
});

describe("host detail", () => {
  it("is not offered for remote desktops, which have no sessions, containers or metrics", () => {
    expect(isHostAssetKind("rdp")).toBe(false);
    expect(isHostAssetKind("vnc")).toBe(false);
    expect(isHostAssetKind("ssh")).toBe(true);
  });
});

describe("telnet kind", () => {
  it("is a terminal-only host without files or a command channel", () => {
    expect(hostToolsSupported("telnet")).toBe(false);
    for (const kind of ["ssh", "local", "winrm", "device", undefined]) expect(hostToolsSupported(kind)).toBe(true);
  });

  it("drops the Telnet options when the asset becomes another kind", () => {
    const options = { localEcho: "on", sendCrlf: true, encoding: "gbk", icon: "router" };
    expect(optionsForKindChange("telnet", "ssh", options)).toEqual({ encoding: "gbk", icon: "router" });
    expect(optionsForKindChange("telnet", "rdp", options)).toEqual({ icon: "router" });
    expect(optionsForKindChange("telnet", "telnet", options)).toEqual(options);
  });
});
