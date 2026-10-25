/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import golden from "../../internal/sync/testdata/compressed_transcript_golden.json";
import {
  COMPRESSED_TRANSCRIPT_LABEL,
  MAX_COMPRESSED_DECODED_BYTES,
  aesGcmSeal,
  base64ToBytes,
  inflateTranscriptPlaintext,
  tryOpenSyncObject,
  utf8Bytes,
} from "../features/auth/crypto";

function hexBytes(hex: string): Uint8Array {
  return Uint8Array.from(hex.match(/../g)!.map((b) => parseInt(b, 16)));
}

async function gzip(body: Uint8Array): Promise<Uint8Array> {
  const deflater = new CompressionStream("gzip");
  const writer = deflater.writable.getWriter();
  void writer.write(body as unknown as BufferSource);
  void writer.close();
  const reader = deflater.readable.getReader();
  const parts: Uint8Array[] = [];
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
  }
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

async function frame(declared: bigint, body: Uint8Array, magic = "NXZ1"): Promise<Uint8Array> {
  const compressed = await gzip(body);
  const out = new Uint8Array(12 + compressed.length);
  out.set(utf8Bytes(magic), 0);
  new DataView(out.buffer).setBigUint64(4, declared, false);
  out.set(compressed, 12);
  return out;
}

async function sealLabel(dek: Uint8Array, plaintext: Uint8Array, id: string, label: string): Promise<Uint8Array> {
  return aesGcmSeal(dek, plaintext, utf8Bytes(`nexterm/go/sync-object/v1\x00${id}\x00${label}`));
}

describe("compressed transcript objects", () => {
  const dek = hexBytes(golden.dekHex);
  const plaintext = base64ToBytes(golden.plaintextB64);

  it("decodes the Go-generated golden vector and the legacy encoding to the same JSON", async () => {
    expect(golden.compressedLabel).toBe(COMPRESSED_TRANSCRIPT_LABEL);
    const compressed = await tryOpenSyncObject(dek, base64ToBytes(golden.compressedBlobB64), golden.id);
    const legacy = await tryOpenSyncObject(dek, base64ToBytes(golden.legacyBlobB64), golden.id);
    expect(compressed.kind).toBe("transcript");
    expect(legacy.kind).toBe("transcript");
    expect(Array.from(compressed.plaintext)).toEqual(Array.from(plaintext));
    expect(Array.from(legacy.plaintext)).toEqual(Array.from(plaintext));
    expect(base64ToBytes(golden.compressedBlobB64).length).toBeLessThan(base64ToBytes(golden.legacyBlobB64).length);
  });

  it("rejects a compressed blob opened with the wrong id", async () => {
    await expect(tryOpenSyncObject(dek, base64ToBytes(golden.compressedBlobB64), "other")).rejects.toMatchObject({ code: "decrypt" });
  });

  it("does not open the legacy zstd label (fails visibly as undecryptable)", async () => {
    const blob = await sealLabel(dek, await frame(BigInt(plaintext.length), plaintext), "tr-z", "transcript.zstd");
    await expect(tryOpenSyncObject(dek, blob, "tr-z")).rejects.toMatchObject({ code: "decrypt" });
  });

  it("rejects bombs, lying headers and malformed frames", async () => {
    const body = new Uint8Array(4 << 20).fill(65);
    const good = await frame(BigInt(body.length), body);
    await expect(inflateTranscriptPlaintext(good)).resolves.toHaveLength(body.length);
    const cases: Record<string, Uint8Array> = {
      "declared smaller": await frame(1024n, body),
      "declared larger": await frame(BigInt(body.length) + 1n, body),
      "over cap": await frame(BigInt(MAX_COMPRESSED_DECODED_BYTES) + 1n, body),
      zero: await frame(0n, body),
      "huge u64": await frame(0xffffffffffffffffn, body),
      "bad magic": await frame(BigInt(body.length), body, "XXXX"),
      truncated: good.subarray(0, 40),
      "header only": good.subarray(0, 12),
      "not gzip": Uint8Array.from([...good.subarray(0, 12), ...utf8Bytes("not gzip at all")]),
    };
    for (const [name, packed] of Object.entries(cases)) {
      await expect(inflateTranscriptPlaintext(packed), name).rejects.toMatchObject({ code: "decrypt" });
    }
  });

  it("enforces a caller-supplied limit through sealed objects", async () => {
    const body = utf8Bytes("x".repeat(2048));
    const blob = await sealLabel(dek, await frame(BigInt(body.length), body), "tr-ok", COMPRESSED_TRANSCRIPT_LABEL);
    const opened = await tryOpenSyncObject(dek, blob, "tr-ok");
    expect(opened.plaintext).toHaveLength(2048);
    await expect(inflateTranscriptPlaintext(await frame(BigInt(body.length), body), 1024)).rejects.toMatchObject({ code: "decrypt" });
  });
});
