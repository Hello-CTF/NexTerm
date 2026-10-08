import { describe, expect, it } from "vitest";
import { formatBytes, formatTime } from "../../ui/format";
import {
  formatBytes as fleetFormatBytes,
  formatTime as fleetFormatTime,
} from "../../features/fleet/format";

describe("formatTime", () => {
  it("0 显示从未", () => {
    expect(formatTime(0)).toBe("从未");
  });

  it("统一为 YYYY-MM-DD HH:mm 本地时间", () => {
    expect(formatTime(new Date(2026, 0, 2, 3, 4).getTime())).toBe("2026-01-02 03:04");
    expect(formatTime(new Date(2026, 11, 31, 23, 59).getTime())).toBe("2026-12-31 23:59");
  });
});

describe("formatBytes", () => {
  it("非法输入显示占位符", () => {
    expect(formatBytes(Number.NaN)).toBe("—");
    expect(formatBytes(-1)).toBe("—");
  });

  it("按 1024 进位并保留一位小数", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1.0 KiB");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(150 * 1024)).toBe("150 KiB");
    expect(formatBytes(2 * 1024 * 1024)).toBe("2.0 MiB");
  });
});

describe("fleet/format re-export", () => {
  it("与 ui/format 是同一实现", () => {
    expect(fleetFormatBytes).toBe(formatBytes);
    expect(fleetFormatTime).toBe(formatTime);
  });
});
