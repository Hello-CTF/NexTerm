/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { dialogLevelForKind } from "../app/App";

describe("dialogLevelForKind (shared App ask mapping)", () => {
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
