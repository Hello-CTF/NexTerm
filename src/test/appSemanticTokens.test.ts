import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const app = readFileSync(new URL("../app/App.tsx", import.meta.url), "utf8");

const RAW_HEX = /#[0-9a-fA-F]{3,8}/;
const RAW_RGB = /rgba?\(/;

describe("App-level semantic tokens", () => {
  it("keeps App.tsx free of raw color literals", () => {
    expect(app).not.toMatch(RAW_HEX);
    expect(app).not.toMatch(RAW_RGB);
  });

  it("guard rejects raw colors inside Tailwind arbitrary values", () => {
    const samples = [
      "shadow-[0_18px_48px_-16px_rgba(0,0,0,0.75)]",
      "bg-[rgb(1,2,3)]",
      "text-[#a1b2c3]/80",
      "border-[#abc]",
    ];
    for (const sample of samples) {
      expect(RAW_RGB.test(sample) || RAW_HEX.test(sample), sample).toBe(true);
    }
  });

  it("styles the success toast from theme tokens", () => {
    expect(app).toContain("nx-alert-success");
  });

  it("references the runtime shadow token instead of the statically expanded utility", () => {
    expect(app).toContain("shadow-[var(--shadow-pop)]");
    expect(app).not.toMatch(/["' ]shadow-pop["' ]/);
  });
});
