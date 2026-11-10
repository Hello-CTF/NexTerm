import { describe, expect, it } from "vitest";
import {
  createQuickSelectInput,
  findQuickSelectMatches,
  labelQuickSelectMatches,
  type QuickSelectMatch,
} from "../../features/terminal/quickSelect";
import type { TextRow } from "../../features/terminal/terminalLinkProvider";

const rows = (...texts: string[]): TextRow[] => texts.map((text) => ({ text, isWrapped: false }));
const POSIX = { windows: false, relative: true };

describe("quick select matches", () => {
  it("finds URLs, paths, hashes, addresses, UUIDs and container ids", () => {
    const matches = findQuickSelectMatches(
      rows(
        "3e13226f fix(session): keep idle sessions https://example.com/pr/1",
        "edit src/app/main.ts:12 on 192.168.1.20 and fe80::1ff:fe23:4567:890a",
        "id 123e4567-e89b-12d3-a456-426614174000",
        "c0ffee0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
      ),
      120,
      POSIX,
    );
    expect(matches.map((m) => [m.kind, m.text])).toEqual([
      ["hash", "3e13226f"],
      ["url", "https://example.com/pr/1"],
      ["path", "src/app/main.ts:12"],
      ["ipv4", "192.168.1.20"],
      ["ipv6", "fe80::1ff:fe23:4567:890a"],
      ["uuid", "123e4567-e89b-12d3-a456-426614174000"],
      ["container", "c0ffee0123456789abcdef0123456789abcdef0123456789abcdef0123456789"],
    ]);
  });

  it("does not take words, numbers, times or invalid addresses", () => {
    const matches = findQuickSelectMatches(rows("deadline facade 1234567890 12:30:45 999.1.1.1 aa:bb:cc:dd:ee:ff"), 80, POSIX);
    expect(matches).toEqual([]);
  });

  it("leaves paths out when path links are off for the terminal", () => {
    const matches = findQuickSelectMatches(rows("vim /etc/hosts"), 80, null);
    expect(matches).toEqual([]);
  });

  it("does not offer the UUID's hex groups or the URL's path separately", () => {
    const matches = findQuickSelectMatches(rows("https://x.io/a/0123abcd.js 123e4567-e89b-12d3-a456-426614174000"), 80, POSIX);
    expect(matches.map((m) => m.kind)).toEqual(["url", "uuid"]);
  });
});

const match = (text: string, row: number): QuickSelectMatch => ({ kind: "hash", text, segments: [{ row, start: 0, end: text.length }] });

describe("quick select labels", () => {
  it("labels from the bottom and shares a label between repeats", () => {
    const labeled = labelQuickSelectMatches([match("aaa1111", 0), match("bbb2222", 1), match("aaa1111", 2)]);
    expect(labeled.map((m) => [m.text, m.label])).toEqual([
      ["aaa1111", "a"],
      ["bbb2222", "s"],
      ["aaa1111", "a"],
    ]);
  });

  it("uses two-letter labels, none a prefix of another, past one alphabet", () => {
    const labeled = labelQuickSelectMatches(Array.from({ length: 30 }, (_, i) => match(`abc${1000 + i}`, i)));
    const labels = labeled.map((m) => m.label);
    expect(new Set(labels).size).toBe(30);
    expect(labels.every((l) => l.length === 2)).toBe(true);
  });
});

describe("quick select keys", () => {
  const labeled = labelQuickSelectMatches([match("aaa1111", 0), match("bbb2222", 1)]);

  it("selects with a label and inserts with Shift", () => {
    expect(createQuickSelectInput(labeled).press({ key: "s" })).toMatchObject({ kind: "select", match: { text: "aaa1111" }, insert: false });
    expect(createQuickSelectInput(labeled).press({ key: "A", shiftKey: true })).toMatchObject({ kind: "select", match: { text: "bbb2222" }, insert: true });
  });

  it("cancels on Escape and ignores other keys", () => {
    const input = createQuickSelectInput(labeled);
    expect(input.press({ key: "z" })).toEqual({ kind: "ignore" });
    expect(input.press({ key: "a", ctrlKey: true })).toEqual({ kind: "ignore" });
    expect(input.press({ key: "Escape" })).toEqual({ kind: "cancel" });
  });

  it("waits for the second letter of a two-letter label and undoes with Backspace", () => {
    const many = labelQuickSelectMatches(Array.from({ length: 30 }, (_, i) => match(`abc${1000 + i}`, i)));
    const input = createQuickSelectInput(many);
    const target = many[many.length - 1];
    expect(input.press({ key: target.label[0] })).toEqual({ kind: "pending", typed: target.label[0] });
    expect(input.press({ key: "Backspace" })).toEqual({ kind: "pending", typed: "" });
    input.press({ key: target.label[0] });
    expect(input.press({ key: target.label[1].toUpperCase(), shiftKey: true })).toMatchObject({ kind: "select", match: { text: target.text }, insert: true });
  });
});
