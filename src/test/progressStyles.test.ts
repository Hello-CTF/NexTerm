/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { declared, installCompiledCSS, styleRules } from "./lib/cssom";

let installed: HTMLStyleElement;
let rules: CSSStyleRule[];

beforeAll(async () => {
  installed = await installCompiledCSS("src/styles.css");
  rules = styleRules(installed);
});

afterAll(() => installed?.remove());

describe("progress bar layout", () => {
  it("uses a block bar so width and height take effect", () => {
    expect(declared(rules, ".nx-progress-bar", "display")).toBe("block");
  });
});
