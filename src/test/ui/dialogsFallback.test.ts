/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { askChoice } from "../../ui/dialogs";

describe("askChoice fallback without registered handlers", () => {
  it("resolves null and never loops sequential native confirms", async () => {
    const confirms: string[] = [];
    const original = window.confirm;
    window.confirm = (message?: string) => {
      confirms.push(String(message));
      return true;
    };
    try {
      const result = await askChoice("How should this be handled?", {
        level: "warning",
        choices: [
          { key: "detach", label: "Keep running in background" },
          { key: "kill", label: "End process", danger: true },
        ],
      });
      expect(result).toBeNull();
      expect(confirms).toEqual([]);
    } finally {
      window.confirm = original;
    }
  });
});
