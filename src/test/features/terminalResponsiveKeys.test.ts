import { describe, expect, it } from "vitest";
import {
  DEFAULT_TERMINAL_KEY_IDS,
  TERMINAL_KEY_CATALOG,
  formatKeySequence,
  resolveTerminalKeys,
  terminalKeySequence,
} from "../../features/terminal/terminalKeys";

const NO_MOD = { ctrl: false, alt: false };
const CTRL = { ctrl: true, alt: false };
const ALT = { ctrl: false, alt: true };
const CTRL_ALT = { ctrl: true, alt: true };

function catalogKey(id: string) {
  const key = TERMINAL_KEY_CATALOG.find((candidate) => candidate.id === id);
  if (!key) throw new Error(`catalog key missing: ${id}`);
  return key;
}

describe("mobile terminal key sequences", () => {
  it("emits navigation and editing keys without depending on a hardware keyboard", () => {
    expect(terminalKeySequence(catalogKey("escape"), NO_MOD)).toBe("\x1b");
    expect(terminalKeySequence(catalogKey("up"), NO_MOD)).toBe("\x1b[A");
    expect(terminalKeySequence(catalogKey("home"), NO_MOD)).toBe("\x1b[H");
    expect(terminalKeySequence(catalogKey("c"), NO_MOD)).toBe("c");
  });

  it("applies one-shot Ctrl sequences without bypassing terminal input handling", () => {
    expect(terminalKeySequence(catalogKey("c"), CTRL)).toBe("\x03");
    expect(terminalKeySequence(catalogKey("d"), CTRL)).toBe("\x04");
    expect(terminalKeySequence(catalogKey("z"), CTRL)).toBe("\x1a");
    expect(terminalKeySequence(catalogKey("up"), CTRL)).toBe("\x1b[A");
  });

  it("keeps every legacy default key with its original byte sequence", () => {
    const legacy: [string, string][] = [
      ["escape", "\x1b"],
      ["tab", "\t"],
      ["up", "\x1b[A"],
      ["down", "\x1b[B"],
      ["right", "\x1b[C"],
      ["left", "\x1b[D"],
      ["home", "\x1b[H"],
      ["end", "\x1b[F"],
      ["c", "c"],
      ["d", "d"],
      ["z", "z"],
    ];
    for (const [id, data] of legacy) {
      expect(terminalKeySequence(catalogKey(id), NO_MOD)).toBe(data);
    }
    expect(terminalKeySequence(catalogKey("c"), CTRL)).toBe("\x03");
    expect(terminalKeySequence(catalogKey("d"), CTRL)).toBe("\x04");
    expect(terminalKeySequence(catalogKey("z"), CTRL)).toBe("\x1a");
  });

  it("adds PageUp/PageDown and common Ctrl combinations to the catalog", () => {
    expect(terminalKeySequence(catalogKey("pageup"), NO_MOD)).toBe("\x1b[5~");
    expect(terminalKeySequence(catalogKey("pagedown"), NO_MOD)).toBe("\x1b[6~");
    expect(terminalKeySequence(catalogKey("ctrl-l"), NO_MOD)).toBe("\x0c");
    expect(terminalKeySequence(catalogKey("ctrl-r"), NO_MOD)).toBe("\x12");
    expect(terminalKeySequence(catalogKey("ctrl-a"), NO_MOD)).toBe("\x01");
    expect(terminalKeySequence(catalogKey("ctrl-e"), NO_MOD)).toBe("\x05");
    expect(terminalKeySequence(catalogKey("ctrl-u"), NO_MOD)).toBe("\x15");
    expect(terminalKeySequence(catalogKey("ctrl-k"), NO_MOD)).toBe("\x0b");
    expect(terminalKeySequence(catalogKey("ctrl-w"), NO_MOD)).toBe("\x17");
  });

  it("composes the sticky Alt modifier as an ESC prefix on the effective sequence", () => {
    expect(terminalKeySequence(catalogKey("c"), ALT)).toBe("\x1bc");
    expect(terminalKeySequence(catalogKey("up"), ALT)).toBe("\x1b\x1b[A");
    expect(terminalKeySequence(catalogKey("pageup"), ALT)).toBe("\x1b\x1b[5~");
    expect(terminalKeySequence(catalogKey("ctrl-l"), ALT)).toBe("\x1b\x0c");
    expect(terminalKeySequence(catalogKey("tab"), ALT)).toBe("\x1b\t");
  });

  it("composes Ctrl and Alt together without dropping either modifier", () => {
    expect(terminalKeySequence(catalogKey("c"), CTRL_ALT)).toBe("\x1b\x03");
    expect(terminalKeySequence(catalogKey("d"), CTRL_ALT)).toBe("\x1b\x04");
    expect(terminalKeySequence(catalogKey("ctrl-r"), CTRL_ALT)).toBe("\x1b\x12");
  });

  it("defaults keep the legacy keys in order and add PgUp/PgDn plus Ctrl+L/Ctrl+R", () => {
    const legacyOrder = ["escape", "tab", "up", "down", "right", "left", "home", "end", "c", "d", "z"];
    const defaultIds = [...DEFAULT_TERMINAL_KEY_IDS];
    const legacyRelative = defaultIds.filter((id) => legacyOrder.includes(id));
    expect(legacyRelative).toEqual(legacyOrder);
    for (const id of ["pageup", "pagedown", "ctrl-l", "ctrl-r"]) {
      expect(defaultIds).toContain(id);
    }
    const resolved = resolveTerminalKeys(defaultIds);
    expect(resolved).toHaveLength(defaultIds.length);
    expect(resolved.map((key) => key.id)).toEqual(defaultIds);
  });

  it("resolves only known catalog ids and skips unknown ones", () => {
    const resolved = resolveTerminalKeys(["escape", "not-a-key", "ctrl-r", "not-a-key"]);
    expect(resolved.map((key) => key.id)).toEqual(["escape", "ctrl-r"]);
  });

  it("renders sequences in caret notation for the config UI", () => {
    expect(formatKeySequence("\x1b[5~")).toBe("^[[5~");
    expect(formatKeySequence("\x03")).toBe("^C");
    expect(formatKeySequence("\t")).toBe("Tab");
    expect(formatKeySequence("\x1bc")).toBe("^[c");
    expect(formatKeySequence("\x1b[H")).toBe("^[[H");
  });
});
