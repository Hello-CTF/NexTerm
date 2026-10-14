import { describe, expect, it } from "vitest";
import { formatBytes, formatTime } from "../../ui/format";
import {
  formatBytes as fleetFormatBytes,
  formatTime as fleetFormatTime,
} from "../../features/fleet/format";

describe("formatTime", () => {
  it("returns the never placeholder for 0", () => {
    expect(formatTime(0)).toBe("从未");
  });

  it("formats local time as YYYY-MM-DD HH:mm", () => {
    expect(formatTime(new Date(2026, 0, 2, 3, 4).getTime())).toBe("2026-01-02 03:04");
    expect(formatTime(new Date(2026, 11, 31, 23, 59).getTime())).toBe("2026-12-31 23:59");
  });
});

describe("formatBytes", () => {
  it("shows a placeholder for invalid input", () => {
    expect(formatBytes(Number.NaN)).toBe("—");
    expect(formatBytes(-1)).toBe("—");
  });

  it("uses 1024-based units with one decimal place", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1.0 KiB");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(150 * 1024)).toBe("150 KiB");
    expect(formatBytes(2 * 1024 * 1024)).toBe("2.0 MiB");
  });
});

describe("fleet/format re-export", () => {
  it("uses the same implementation as ui/format", () => {
    expect(fleetFormatBytes).toBe(formatBytes);
    expect(fleetFormatTime).toBe(formatTime);
  });
});
