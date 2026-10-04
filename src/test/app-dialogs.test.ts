/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { dialogLevelForKind } from "../app/App";

describe("dialogLevelForKind（App 共享 ask 映射）", () => {
  it("explicit info maps to info", () => {
    expect(dialogLevelForKind("info")).toBe("info");
  });

  it("missing kind defaults to warning", () => {
    expect(dialogLevelForKind(undefined)).toBe("warning");
  });

  it("warning and error map to warning", () => {
    expect(dialogLevelForKind("warning")).toBe("warning");
    expect(dialogLevelForKind("error")).toBe("warning");
  });
});
