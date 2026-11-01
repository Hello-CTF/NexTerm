import { describe, expect, it } from "vitest";
import { ASSET_KIND_FIELDS } from "../../features/explorer/assetKindFields";
import {
  canWake,
  hasWakeErrors,
  readWakeOptions,
  validateWakeBroadcast,
  validateWakeMac,
  validateWakeOptions,
  validateWakePort,
  writeWakeOptions,
} from "../../features/explorer/wakeOptions";

describe("wake options", () => {
  it("is offered for the network kinds only", () => {
    const withWake = Object.entries(ASSET_KIND_FIELDS)
      .filter(([, fields]) => fields.wake)
      .map(([kind]) => kind)
      .sort();
    expect(withWake).toEqual(["docker", "rdp", "ssh", "vnc", "winrm"]);
  });

  it("validates MAC addresses strictly", () => {
    for (const ok of ["AA:BB:CC:DD:EE:01", "aa-bb-cc-dd-ee-01", "", "  00:11:22:33:44:55 "]) expect(validateWakeMac(ok)).toBeNull();
    for (const bad of ["aabbccddee01", "aa:bb:cc:dd:ee", "aa:bb-cc:dd:ee:01", "aa:bb:cc:dd:ee:0g", "aabb.ccdd.ee01", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "a:b:c:d:e:f"]) {
      expect(validateWakeMac(bad), bad).not.toBeNull();
    }
  });

  it("validates the broadcast address and port like the backend", () => {
    expect(validateWakeBroadcast("")).toBeNull();
    expect(validateWakeBroadcast("192.168.1.255")).toBeNull();
    expect(validateWakeBroadcast("255.255.255.255")).toBeNull();
    expect(validateWakeBroadcast("8.8.8.8")).toBeNull();
    expect(validateWakeBroadcast("100.63.0.1")).toBeNull();
    for (const bad of ["host.example", "::1", "127.0.0.1", "0.0.0.0", "224.0.0.1", "169.254.169.254", "1.2.3", "256.1.1.1", "010.0.0.1", "10.0.0.01", "100.64.0.1", "100.127.1.1", "240.0.0.1", "255.255.255.254"]) {
      expect(validateWakeBroadcast(bad), bad).not.toBeNull();
    }
    expect(validateWakePort("")).toBeNull();
    expect(validateWakePort("7")).toBeNull();
    for (const bad of ["0", "70000", "9x", "-1", "1.5"]) expect(validateWakePort(bad), bad).not.toBeNull();
  });

  it("ignores broadcast and port without a MAC and reports errors with one", () => {
    expect(hasWakeErrors(validateWakeOptions({ mac: "", broadcast: "bad", port: "x" }))).toBe(false);
    expect(hasWakeErrors(validateWakeOptions({ mac: "AA:BB:CC:DD:EE:01", broadcast: "bad", port: "" }))).toBe(true);
  });

  it("round-trips through the stored options and keeps unrelated keys", () => {
    const written = writeWakeOptions({ viaAssetId: "device/d1", domain: "x" }, { mac: " AA:BB:CC:DD:EE:01 ", broadcast: "10.0.0.255", port: "7" });
    expect(written).toEqual({ viaAssetId: "device/d1", domain: "x", wakeMac: "AA:BB:CC:DD:EE:01", wakeBroadcast: "10.0.0.255", wakePort: 7 });
    expect(readWakeOptions(written)).toEqual({ mac: "AA:BB:CC:DD:EE:01", broadcast: "10.0.0.255", port: "7" });
    expect(canWake(written)).toBe(true);
    const cleared = writeWakeOptions(written, { mac: "", broadcast: "10.0.0.255", port: "7" });
    expect(cleared).toEqual({ viaAssetId: "device/d1", domain: "x" });
    expect(canWake(cleared)).toBe(false);
    expect(writeWakeOptions({}, { mac: "AA:BB:CC:DD:EE:01", broadcast: "", port: "" })).toEqual({ wakeMac: "AA:BB:CC:DD:EE:01" });
  });
});
