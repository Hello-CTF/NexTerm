import { describe, expect, it } from "vitest";
import { bytesFromBase64, bytesToBase64, decodeContent } from "../../features/files/fileCodec";

function bytes(length: number): Uint8Array {
  return Uint8Array.from({ length }, (_, i) => (i * 31 + 17) % 256);
}

function sameBytes(actual: Uint8Array, expected: Uint8Array): boolean {
  if (actual.length !== expected.length) return false;
  for (let i = 0; i < expected.length; i += 1) {
    if (actual[i] !== expected[i]) return false;
  }
  return true;
}

describe("base64 codec", () => {
  for (const length of [0, 1, 2, 3, 32_767, 32_768, 32_769, 65_535, 65_536, 65_537, 5 * 1024 * 1024 + 1]) {
    it(`round-trips ${length} bytes without interior padding`, () => {
      const source = bytes(length);
      const encoded = bytesToBase64(source);
      const padding = encoded.indexOf("=");
      if (padding >= 0) expect(padding).toBeGreaterThanOrEqual(encoded.length - 2);
      const decoded = bytesFromBase64(encoded);
      expect(decoded.length).toBe(source.length);
      expect(sameBytes(decoded, source)).toBe(true);
    });
  }
});

describe("strict text decoding", () => {
  it("decodes UTF-8 and falls back to valid GBK in auto mode", () => {
    expect(decodeContent(new TextEncoder().encode("中文"), "auto")).toEqual({
      text: "中文",
      encoding: "utf-8",
    });
    expect(decodeContent(Uint8Array.from([0xd6, 0xd0, 0xce, 0xc4]), "auto")).toEqual({
      text: "中文",
      encoding: "gbk",
    });
  });

  it("replaces no malformed input with U+FFFD", () => {
    expect(() => decodeContent(Uint8Array.from([0xc3, 0x28]), "utf-8")).toThrow(/UTF-8/);
    expect(() => decodeContent(Uint8Array.from([0xff]), "gbk")).toThrow(/GBK/);
    expect(() => decodeContent(Uint8Array.from([0xff]), "auto")).toThrow(/GBK/);
  });
});
