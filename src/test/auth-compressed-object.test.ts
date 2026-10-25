/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import golden from "../../internal/sync/testdata/compressed_transcript_golden.json";
import {
  COMPRESSED_TRANSCRIPT_LABEL,
  MAX_COMPRESSED_DECODED_BYTES,
  PACKED_TRANSCRIPT_LABEL,
  aesGcmOpen,
  aesGcmSeal,
  base64ToBytes,
  inflateTranscriptPlaintext,
  tryOpenSyncObject,
  unpackTranscriptBody,
  unpackTranscriptPlaintext,
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

function objectAAD(id: string, label: string): Uint8Array {
  return utf8Bytes(`nexterm/go/sync-object/v1\x00${id}\x00${label}`);
}

function uvarint(value: number): number[] {
  const out: number[] = [];
  while (value >= 0x80) {
    out.push((value % 0x80) | 0x80);
    value = Math.floor(value / 0x80);
  }
  out.push(value);
  return out;
}

function packBody(skeleton: string, lengths: number[], data: string, slots = lengths.length, magic = "NXP1"): Uint8Array {
  const skeletonBytes = utf8Bytes(skeleton);
  return Uint8Array.from([
    ...utf8Bytes(magic),
    ...uvarint(skeletonBytes.length),
    ...skeletonBytes,
    ...uvarint(slots),
    ...lengths.flatMap(uvarint),
    ...utf8Bytes(data),
  ]);
}

function withDeclared(packed: Uint8Array, declared: bigint, frame = packed.subarray(12)): Uint8Array {
  const out = new Uint8Array(12 + frame.length);
  out.set(packed.subarray(0, 4), 0);
  new DataView(out.buffer).setBigUint64(4, declared, false);
  out.set(frame, 12);
  return out;
}

async function sealLabel(dek: Uint8Array, plaintext: Uint8Array, id: string, label: string): Promise<Uint8Array> {
  return aesGcmSeal(dek, plaintext, utf8Bytes(`nexterm/go/sync-object/v1\x00${id}\x00${label}`));
}

describe("packed transcript objects", () => {
  const dek = hexBytes(golden.dekHex);
  const plaintext = base64ToBytes(golden.plaintextB64);
  const packedBlob = base64ToBytes(golden.packedBlobB64);

  it("rebuilds the exact Go JSON from the Go-generated packed vector", async () => {
    expect(golden.packedLabel).toBe(PACKED_TRANSCRIPT_LABEL);
    const opened = await tryOpenSyncObject(dek, packedBlob, golden.id);
    expect(opened.kind).toBe("transcript");
    expect(Array.from(opened.plaintext)).toEqual(Array.from(plaintext));
    expect(packedBlob.length).toBeLessThan(base64ToBytes(golden.compressedBlobB64).length);
  });

  it("rejects a packed blob opened with the wrong id", async () => {
    await expect(tryOpenSyncObject(dek, packedBlob, "other")).rejects.toMatchObject({ code: "decrypt" });
  });

  it("refills empty, null-adjacent and padded slots exactly", () => {
    const body = packBody('{"c":[{"data":""},{"data":null},{"data":""},{"data":""}],"n":"\\"data\\":\\""}', [3, 0, 2], "abcde");
    expect(new TextDecoder().decode(unpackTranscriptBody(body))).toBe(
      '{"c":[{"data":"YWJj"},{"data":null},{"data":""},{"data":"ZGU="}],"n":"\\"data\\":\\""}',
    );
  });

  it("rejects malformed pack bodies", () => {
    const skeleton = '{"c":[{"data":""},{"data":""}]}';
    const cases: Record<string, Uint8Array> = {
      "bad magic": packBody(skeleton, [3, 2], "abcde", 2, "XXXX"),
      "no slots": packBody(skeleton, [], ""),
      "slot count beyond body": packBody(skeleton, [3, 2], "abcde", 1 << 30),
      "fewer slots declared": packBody(skeleton, [3], "abc"),
      "more slots declared": packBody(skeleton, [3, 2, 1], "abcdef"),
      "lengths exceed data": packBody(skeleton, [3, 9], "abcde"),
      "trailing data": packBody(skeleton, [3, 2], "abcdez"),
      "filled slot": packBody('{"c":[{"data":"x"},{"data":""}]}', [3, 2], "abcde"),
      truncated: packBody(skeleton, [3, 2], "abcde").subarray(0, 8),
    };
    for (const [name, body] of Object.entries(cases)) {
      expect(() => unpackTranscriptBody(body), name).toThrow(expect.objectContaining({ code: "decrypt" }));
    }
    expect(() => unpackTranscriptBody(packBody(skeleton, [3, 2], "abcde"), 8)).toThrow(expect.objectContaining({ code: "decrypt" }));
  });

  it("rejects lying sizes and oversized zstd windows before decoding", async () => {
    const packed = await aesGcmOpen(dek, packedBlob, objectAAD(golden.id, PACKED_TRANSCRIPT_LABEL));
    const declared = new DataView(packed.buffer, packed.byteOffset).getBigUint64(4, false);
    await expect(unpackTranscriptPlaintext(packed)).resolves.toHaveLength(plaintext.length);
    const zstdMagic = [0x28, 0xb5, 0x2f, 0xfd];
    const cases: Record<string, Uint8Array> = {
      "declared smaller": withDeclared(packed, declared - 1n),
      "declared larger": withDeclared(packed, declared + 1n),
      "declared zero": withDeclared(packed, 0n),
      "over cap": withDeclared(packed, BigInt(MAX_COMPRESSED_DECODED_BYTES) + 1n),
      "not zstd": withDeclared(packed, declared, utf8Bytes("not a zstd frame")),
      "huge window": withDeclared(packed, declared, Uint8Array.from([...zstdMagic, 0x00, 0xf8, 0, 0, 0])),
      "huge single segment": withDeclared(packed, declared, Uint8Array.from([...zstdMagic, 0xe0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f])),
      truncated: packed.subarray(0, 30),
    };
    for (const [name, value] of Object.entries(cases)) {
      await expect(unpackTranscriptPlaintext(value), name).rejects.toMatchObject({ code: "decrypt" });
    }
  });
});

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
