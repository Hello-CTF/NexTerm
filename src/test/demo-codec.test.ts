import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";
import { fsFileContent } from "../demo/data";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

function asciiText(length: number): string {
  return Array.from({ length }, (_, i) => String.fromCharCode(32 + (i * 7) % 90)).join("");
}

function utf8Bytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

function refBase64(text: string): string {
  const bytes = utf8Bytes(text);
  const parts: string[] = [];
  for (let i = 0; i < bytes.length; i += 32_768) {
    parts.push(String.fromCharCode(...bytes.subarray(i, i + 32_768)));
  }
  return btoa(parts.join(""));
}

async function demoRead(path: string): Promise<{ path: string; size: number; contentBase64: string }> {
  return (await mockInvoke("fs_read", { args: { path } })) as {
    path: string;
    size: number;
    contentBase64: string;
  };
}

async function demoWrite(path: string, contentBase64: string): Promise<unknown> {
  return mockInvoke("fs_write", { args: { path, contentBase64 } });
}

describe("demo fs_read base64", () => {
  for (const length of [0, 1, 2, 3, 32_765, 32_766, 32_767, 32_768, 32_769, 65_535, 65_536, 65_537]) {
    it(`encodes ${length} bytes exactly as one standards-valid base64 document`, async () => {
      const path = `/home/deploy/codec-read-${length}.txt`;
      const text = asciiText(length);
      fsFileContent[path] = text;
      const res = await demoRead(path);
      expect(res.size).toBe(length);
      expect(res.contentBase64).toBe(refBase64(text));
      const padding = res.contentBase64.indexOf("=");
      if (padding >= 0) expect(padding).toBeGreaterThanOrEqual(res.contentBase64.length - 2);
    });
  }

  it("keeps multi-byte UTF-8 intact across chunk boundaries", async () => {
    const text = "a".repeat(32_765) + "中" + "b".repeat(100);
    const path = "/home/deploy/codec-read-boundary.txt";
    fsFileContent[path] = text;
    const res = await demoRead(path);
    expect(res.size).toBe(utf8Bytes(text).length);
    expect(res.contentBase64).toBe(refBase64(text));
  });

  it("keeps multi-byte UTF-8 intact across multi-MiB content", async () => {
    const text = "中文🙂abc\n".repeat(600_000);
    const path = "/home/deploy/codec-read-big.txt";
    fsFileContent[path] = text;
    const res = await demoRead(path);
    expect(res.size).toBe(utf8Bytes(text).length);
    expect(res.contentBase64).toBe(refBase64(text));
  }, 30_000);
});

describe("demo fs_write decode and error propagation", () => {
  it("round-trips binary-safe UTF-8 content (NUL, multi-byte, large)", async () => {
    const path = "/home/deploy/codec-roundtrip.bin";
    const text = `\0header\n${"中文🙂".repeat(20_000)}\n${asciiText(70_000)}\0`;
    await demoWrite(path, refBase64(text));
    expect(fsFileContent[path]).toBe(text);
    const res = await demoRead(path);
    expect(res.contentBase64).toBe(refBase64(text));
    expect(res.size).toBe(utf8Bytes(text).length);
  });

  it("writes an empty file without inventing content", async () => {
    const path = "/home/deploy/codec-empty.txt";
    await demoWrite(path, "");
    expect(fsFileContent[path]).toBe("");
    const res = await demoRead(path);
    expect(res).toMatchObject({ size: 0, contentBase64: "" });
  });

  it("rejects invalid base64 and leaves the previous content untouched", async () => {
    const path = "/home/deploy/codec-invalid-b64.txt";
    fsFileContent[path] = "original";
    await expect(demoWrite(path, "!!!not-base64!!!")).rejects.toMatchObject({ code: "bad_param" });
    expect(fsFileContent[path]).toBe("original");
  });

  it("rejects interior padding (the historical concatenated-chunks defect shape)", async () => {
    const path = "/home/deploy/codec-padded-chunks.txt";
    fsFileContent[path] = "original";
    await expect(demoWrite(path, "YQ==YQ==")).rejects.toMatchObject({ code: "bad_param" });
    expect(fsFileContent[path]).toBe("original");
  });

  it("rejects invalid UTF-8 instead of silently storing U+FFFD replacements", async () => {
    const path = "/home/deploy/codec-invalid-utf8.txt";
    fsFileContent[path] = "original";
    for (const bytes of [[0xc3, 0x28], [0xff], [0xe4, 0xb8]]) {
      await expect(demoWrite(path, btoa(String.fromCharCode(...bytes)))).rejects.toMatchObject({
        code: "bad_param",
      });
    }
    expect(fsFileContent[path]).toBe("original");
  });
});
