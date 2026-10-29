import { describe, expect, it } from "vitest";
import { progressPercent } from "../../features/files/fileProgress";

describe("transfer progress percentage", () => {
  it("clamps displayed percentages and treats empty totals as zero", () => {
    expect(progressPercent({ transferred: 1, total: 0 })).toBe(0);
    expect(progressPercent({ transferred: 500, total: 200 })).toBe(100);
    expect(progressPercent({ transferred: -10, total: 200 })).toBe(0);
    expect(progressPercent({ transferred: 50, total: 200 })).toBe(25);
  });
});
