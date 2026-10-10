import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  BRACKETED_PASTE_END,
  BRACKETED_PASTE_START,
  confirmMultilinePaste,
  multilinePasteLines,
  wrapBracketedPaste,
} from "../../features/terminal/terminalPaste";
import { ask } from "../../ui/dialogs";

vi.mock("../../ui/dialogs", () => ({ ask: vi.fn() }));

describe("bracketed paste wrapping", () => {
  it("wraps multi-line text only when the application enabled the mode", () => {
    const multiLine = "echo one\necho two\n";
    expect(wrapBracketedPaste(multiLine, true)).toBe(
      `${BRACKETED_PASTE_START}echo one\necho two\n${BRACKETED_PASTE_END}`,
    );
    expect(wrapBracketedPaste(multiLine, false)).toBe(multiLine);
  });

  it("preserves raw bytes otherwise, including carriage returns and escapes", () => {
    const raw = "a\rb\x1b[1;5C\n";
    expect(wrapBracketedPaste(raw, false)).toBe(raw);
    expect(wrapBracketedPaste("", true)).toBe(`${BRACKETED_PASTE_START}${BRACKETED_PASTE_END}`);
  });
});

describe("multilinePasteLines", () => {
  it("treats single-line text and a trailing newline as no confirmation needed", () => {
    expect(multilinePasteLines("ls -la")).toBeNull();
    expect(multilinePasteLines("ls -la\n")).toBeNull();
    expect(multilinePasteLines("ls -la\r\n")).toBeNull();
    expect(multilinePasteLines("")).toBeNull();
  });

  it("splits multi-line text on LF, CR and CRLF", () => {
    expect(multilinePasteLines("echo a\necho b\n")).toEqual(["echo a", "echo b"]);
    expect(multilinePasteLines("a\r\nb\r\nc")).toEqual(["a", "b", "c"]);
    expect(multilinePasteLines("a\rb")).toEqual(["a", "b"]);
    expect(multilinePasteLines("a\n\nb")).toEqual(["a", "", "b"]);
  });
});

describe("confirmMultilinePaste", () => {
  beforeEach(() => {
    vi.mocked(ask).mockReset();
  });

  it("passes single-line pastes through without asking", async () => {
    await expect(confirmMultilinePaste("ls -la")).resolves.toBe(true);
    expect(ask).not.toHaveBeenCalled();
  });

  it("asks with the line count and a capped preview for multi-line pastes", async () => {
    vi.mocked(ask).mockResolvedValue(true);
    const text = ["l1", "l2", "l3", "l4", "l5", "l6", "l7"].join("\n");
    await expect(confirmMultilinePaste(text)).resolves.toBe(true);
    expect(ask).toHaveBeenCalledTimes(1);
    const [message, options] = vi.mocked(ask).mock.calls[0];
    expect(message).toContain("7 行");
    expect(message).toContain("l1");
    expect(message).toContain("l5");
    expect(message).not.toContain("l6");
    expect(message).toContain("…（共 7 行）");
    expect(options).toEqual({ title: "粘贴多行文本", kind: "warning" });
  });

  it("truncates very long lines in the preview", async () => {
    vi.mocked(ask).mockResolvedValue(true);
    await confirmMultilinePaste(`${"x".repeat(200)}\nsecond`);
    const [message] = vi.mocked(ask).mock.calls[0];
    expect(message).toContain(`${"x".repeat(120)}…`);
    expect(message).not.toContain("x".repeat(121));
  });

  it("returns the user's decision", async () => {
    vi.mocked(ask).mockResolvedValue(false);
    await expect(confirmMultilinePaste("a\nb")).resolves.toBe(false);
  });
});
