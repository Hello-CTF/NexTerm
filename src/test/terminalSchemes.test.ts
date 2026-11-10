/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  BUNDLED_SCHEMES,
  contrastRatio,
  importSchemeFile,
  MAX_CUSTOM_SCHEMES,
  MAX_SCHEME_IMPORT_BYTES,
  parseCustomSchemes,
  SchemeImportError,
  stripJsonc,
  schemeKind,
} from "../features/terminal/terminalSchemes";
import {
  DEFAULT_DARK_SCHEME_ID,
  DEFAULT_LIGHT_SCHEME_ID,
  resolveTerminalScheme,
  terminalSchemeCatalog,
  xtermThemeForScheme,
  XTERM_DARK_HIGH_CONTRAST_THEME,
  XTERM_DARK_THEME,
} from "../features/terminal/xtermTheme";

const FIXTURES = "src/test/fixtures/terminal-schemes";

function fixture(name: string): string {
  return readFileSync(`${FIXTURES}/${name}`, "utf8");
}

const ANSI16 = Array.from({ length: 16 }, (_, i) => `#0${i.toString(16)}0000`);

function custom(id: string, overrides: Record<string, unknown> = {}) {
  return { id, name: `Scheme ${id}`, background: "#101010", foreground: "#EEEEEE", ansi: ANSI16, ...overrides };
}

describe("bundled terminal schemes", () => {
  it("keeps readable foreground text on every bundled background", () => {
    // 4:1 admits Solarized Light's published palette (4.1:1); every other bundled scheme exceeds 4.5:1.
    for (const scheme of terminalSchemeCatalog()) {
      expect(contrastRatio(scheme.palette.foreground, scheme.palette.background), scheme.name).toBeGreaterThanOrEqual(4);
    }
  });

  it("defines complete, unique palettes in both light and dark kinds", () => {
    const ids = new Set<string>();
    for (const scheme of BUNDLED_SCHEMES) {
      expect(ids.has(scheme.id), scheme.id).toBe(false);
      ids.add(scheme.id);
      expect(scheme.palette.ansi).toHaveLength(16);
      for (const color of [scheme.palette.background, scheme.palette.foreground, ...scheme.palette.ansi]) {
        expect(color, scheme.id).toMatch(/^#[0-9a-f]{6}$/);
      }
    }
    const catalog = terminalSchemeCatalog();
    expect(catalog.filter((scheme) => scheme.kind === "dark").length).toBeGreaterThanOrEqual(8);
    expect(catalog.filter((scheme) => scheme.kind === "light").length).toBeGreaterThanOrEqual(6);
    expect(resolveTerminalScheme("dark", "catppuccin-mocha").kind).toBe("dark");
    expect(resolveTerminalScheme("light", "catppuccin-latte").kind).toBe("light");
  });

  it("falls back to the NexTerm default for unknown, removed or wrong-kind ids", () => {
    expect(resolveTerminalScheme("dark", "no-such-scheme").id).toBe(DEFAULT_DARK_SCHEME_ID);
    expect(resolveTerminalScheme("dark", "solarized-light").id).toBe(DEFAULT_DARK_SCHEME_ID);
    expect(resolveTerminalScheme("light", "custom-gone").id).toBe(DEFAULT_LIGHT_SCHEME_ID);
  });

  it("keeps the contrast variants for the defaults and the published palette for others", () => {
    const dark = resolveTerminalScheme("dark", DEFAULT_DARK_SCHEME_ID);
    expect(xtermThemeForScheme(dark).background).toBe(XTERM_DARK_THEME.background);
    expect(xtermThemeForScheme(dark, "high").background).toBe(XTERM_DARK_HIGH_CONTRAST_THEME.background);
    expect(xtermThemeForScheme(dark).cursor).toBe("#516cd6");

    const nord = resolveTerminalScheme("dark", "nord");
    const theme = xtermThemeForScheme(nord, "maximum");
    expect(theme.background).toBe("#2e3440");
    expect(theme.foreground).toBe("#d8dee9");
    expect(theme.brightCyan).toBe("#8fbcbb");
    expect(theme.cursor).toBe("#d8dee9");
    expect(theme.cursorAccent).toBe("#2e3440");
    expect(theme.selectionBackground).toBe("#434c5e");
  });
});

describe("custom scheme parsing", () => {
  it("normalizes valid entries and drops invalid or duplicate ones", () => {
    const parsed = parseCustomSchemes([
      custom("custom-a", { cursor: "#ABCDEF" }),
      custom("custom-a"),
      custom("nord"),
      custom("custom-b", { ansi: ANSI16.slice(0, 15) }),
      custom("custom-c", { background: "black" }),
      custom("custom-d", { name: "  " }),
      custom("custom-e", { name: "a\u0007b" }),
      custom("custom-f", { selection: "#fff" }),
      "junk",
      custom("custom-g"),
    ]);
    expect(parsed.map((item) => item.id)).toEqual(["custom-a", "custom-g"]);
    expect(parsed[0].foreground).toBe("#eeeeee");
    expect(parsed[0].cursor).toBe("#abcdef");
    expect(parsed[0]).not.toHaveProperty("selection");
  });

  it("returns an empty list for non-array values and caps the count", () => {
    expect(parseCustomSchemes(null)).toEqual([]);
    expect(parseCustomSchemes({ id: "custom-a" })).toEqual([]);
    const many = Array.from({ length: MAX_CUSTOM_SCHEMES + 5 }, (_, i) => custom(`custom-${i}`));
    expect(parseCustomSchemes(many)).toHaveLength(MAX_CUSTOM_SCHEMES);
  });

  it("lists imported schemes in the slot of their background", () => {
    const [light] = parseCustomSchemes([custom("custom-l", { background: "#fdfdfd", foreground: "#222222" })]);
    expect(schemeKind(light)).toBe("light");
    const catalog = terminalSchemeCatalog([light]);
    expect(catalog.at(-1)).toMatchObject({ id: "custom-l", kind: "light", custom: true });
    expect(resolveTerminalScheme("light", "custom-l", [light]).palette.background).toBe("#fdfdfd");
    expect(resolveTerminalScheme("dark", "custom-l", [light]).id).toBe(DEFAULT_DARK_SCHEME_ID);
  });
});

describe("scheme importers", () => {
  it("imports an iTerm2 .itermcolors plist", () => {
    const [scheme] = importSchemeFile("Dracula.itermcolors", fixture("Dracula.itermcolors"));
    expect(scheme.name).toBe("Dracula");
    expect(scheme.palette.background).toBe("#282a36");
    expect(scheme.palette.foreground).toBe("#f8f8f2");
    expect(scheme.palette.cursor).toBe("#f8f8f2");
    expect(scheme.palette.selection).toBe("#44475a");
    expect(scheme.palette.ansi[1]).toBe("#ff5555");
    expect(scheme.palette.ansi[15]).toBe("#ffffff");
  });

  it("imports a Windows Terminal scheme and every scheme of a settings file", () => {
    const [campbell] = importSchemeFile("campbell.json", fixture("campbell.json"));
    expect(campbell.name).toBe("Campbell");
    expect(campbell.palette.ansi[5]).toBe("#881798");
    expect(campbell.palette.cursor).toBe("#ffffff");
    const all = importSchemeFile("settings.json", fixture("settings.json"));
    expect(all.map((item) => item.name)).toEqual(["Campbell", "One Half Light"]);
    expect(schemeKind(all[1].palette)).toBe("light");
  });

  it("imports a Windows Terminal settings.json written as JSONC", () => {
    const [campbell] = importSchemeFile("settings.json", fixture("settings-jsonc.json"));
    expect(campbell.name).toBe("Campbell");
    expect(campbell.palette.ansi[15]).toBe("#f2f2f2");
    expect(JSON.parse(stripJsonc('{"a": "x // y /* z */", "b": [1, 2, /* c */ ], // d\n}'))).toEqual({ a: "x // y /* z */", b: [1, 2] });
  });

  it("rejects an iTerm2 color with an empty component", () => {
    const text = fixture("Dracula.itermcolors").replace(/(<key>Blue Component<\/key>\s*)<real>[0-9.]+<\/real>/, "$1<real></real>");
    expect(text).toContain("<real></real>");
    expect(() => importSchemeFile("Dracula.itermcolors", text)).toThrow(SchemeImportError);
  });

  it("imports Ghostty and kitty theme files", () => {
    const [nord] = importSchemeFile("nord-ghostty", fixture("nord-ghostty"));
    expect(nord.name).toBe("nord-ghostty");
    expect(nord.palette.background).toBe("#2e3440");
    expect(nord.palette.cursor).toBe("#eceff4");
    expect(nord.palette.selection).toBe("#434c5e");
    expect(nord.palette.ansi[14]).toBe("#8fbcbb");

    const [mocha] = importSchemeFile("mocha-kitty.conf", fixture("mocha-kitty.conf"));
    expect(mocha.name).toBe("mocha-kitty");
    expect(mocha.palette.background).toBe("#1e1e2e");
    expect(mocha.palette.cursor).toBe("#f5e0dc");
    expect(mocha.palette.ansi[8]).toBe("#585b70");
  });

  it("rejects incomplete, malformed and oversized files", () => {
    expect(() => importSchemeFile("incomplete.conf", fixture("incomplete.conf"))).toThrow(SchemeImportError);
    expect(() => importSchemeFile("bad.json", "{not json")).toThrow(SchemeImportError);
    expect(() => importSchemeFile("bad.itermcolors", "<plist><dict>")).toThrow(SchemeImportError);
    expect(() => importSchemeFile("empty.json", "[]")).toThrow(SchemeImportError);
    expect(() => importSchemeFile("big.conf", "#".repeat(MAX_SCHEME_IMPORT_BYTES + 1))).toThrow(SchemeImportError);
  });
});
