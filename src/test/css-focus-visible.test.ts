import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const a11y = readFileSync(new URL("../ui/a11y.css", import.meta.url), "utf8");

describe("focus-visible contract", () => {
  it("does not draw a focus ring for the auto-focused command palette input", () => {
    expect(a11y).toMatch(
      /\.nx-overlay \.nx-command-input:focus-visible\s*\{[^}]*outline:\s*none/,
    );
  });

  it("draws a focus ring when a CodeMirror editor is focused", () => {
    expect(css).toMatch(
      /\.cm-editor\.cm-focused:focus-within\s*\{[^}]*outline:\s*2px solid var\(--nx-focus-ring\)[^}]*outline-offset:\s*-2px/,
    );
  });

  it("draws a focus ring when a CodeMirror panel input is focused", () => {
    expect(css).toMatch(
      /\.cm-editor \.cm-textfield:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--nx-focus-ring\)/,
    );
  });

  it("maps overlay focus rings to Highlight in forced-colors mode", () => {
    const block = a11y.match(/@media \(forced-colors: active\) \{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    expect(block![1]).toMatch(/outline-color:\s*Highlight/);
  });
});
