/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { declared, installCompiledCSS, styleRules } from "../lib/cssom";

describe("app header on small windows", () => {
  it("drops the app name, which the rail logo already shows, and gives the room to the workspace switcher", async () => {
    const installed = await installCompiledCSS("src/styles.css");
    try {
      const rules = styleRules(installed);
      const small = "(max-width: 720px)";
      expect(declared(rules, ".nx-app-header .nx-app-name", "display", small)).toBe("none");
      expect(declared(rules, ".nx-app-header .nx-ws-switch", "max-width", small)).toBe("min(260px, 52vw)");
      // On a wide window the name stays.
      expect(declared(rules, ".nx-app-header .nx-app-name", "display", null)).toBeUndefined();
    } finally {
      installed.remove();
    }
  });
});
