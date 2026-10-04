import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const app = readFileSync(new URL("../app/App.tsx", import.meta.url), "utf8");

describe("App-level semantic tokens", () => {
  it("keeps App.tsx free of raw color literals", () => {
    expect(app).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(app).not.toMatch(/\brgba?\(/);
  });

  it("styles the success toast from theme tokens", () => {
    expect(app).toContain("bg-[color-mix(in_srgb,var(--color-green-500)_18%,var(--nx-bg-pane))]");
  });
});
