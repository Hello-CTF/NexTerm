import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

describe("progress bar layout", () => {
  it("uses a block bar so width and height take effect", () => {
    expect(css).toMatch(/\.nx-progress-bar\s*\{[^}]*display:\s*block;/);
  });
});
