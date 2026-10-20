/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import {
  base64ToBytes,
  bytesToBase64,
  bytesToHex,
  formatRecoveryKey,
  generateDEK,
  generateRecoveryKey,
  normalizeRecoveryKey,
  objectPayloadHash,
  openSyncObject,
  recoveryKeyHash,
  sealSyncObject,
  tryOpenSyncObject,
  unwrapDEKWithPassword,
  unwrapDEKWithRecovery,
  utf8Bytes,
  wrapDEKWithPassword,
  wrapDEKWithRecovery,
} from "../features/auth/crypto";

// Real Go-side vault.GenerateUserDEKEnvelopes output (scripts/syncv2-helper gen-dek, generated once).
const GO_FIXTURE = {
  password: "test-password-123",
  dekHex: "c26ae1b3822f6f6dc4da86e69b7d5e8fde0b0fa29ddd6481716db07fcce032ad",
  dekEnvelope: "09TqgMTkQf7UvK2nzp5u3S48ggs0d/yHM4xh5rC5OYbGkUJfNEnYnzcNgOsSRkMAK8MoUNmkU0GKfMwl",
  kdfSalt: "0lDbbAdGDsSjmcAs/vsNtQ==",
  kdfParams: '{"t":3,"m":65536,"p":4}',
  recoveryEnvelope: "PR04goBuVFxFwswIg+lsgZaQySPbm2/ft+4gRl6YFvgUW4JQhoaXLMscw+yOrXhX4+mkcKKh5EnZQJwK",
  recoveryHash: "bf19cc2446a331d83b984666f621e480196ba373402a91e4adad34ea7ac0df82",
};

describe("DEK envelope interoperability with the Go vault", () => {
  it("unwraps a Go password envelope using production argon2id parameters", async () => {
    const dek = await unwrapDEKWithPassword(
      GO_FIXTURE.password,
      base64ToBytes(GO_FIXTURE.dekEnvelope),
      base64ToBytes(GO_FIXTURE.kdfSalt),
      GO_FIXTURE.kdfParams,
    );
    expect(bytesToHex(dek)).toBe(GO_FIXTURE.dekHex);
  }, 60_000);

  it("round-trips a local password-wrapped envelope", async () => {
    const dek = generateDEK();
    const { envelope, salt, params } = await wrapDEKWithPassword("correct horse battery staple", dek);
    expect(params).toBe('{"t":3,"m":65536,"p":4}');
    const opened = await unwrapDEKWithPassword("correct horse battery staple", envelope, salt, params);
    expect(bytesToHex(opened)).toBe(bytesToHex(dek));
    await expect(unwrapDEKWithPassword("wrong password", envelope, salt, params)).rejects.toThrow();
  }, 180_000);

  it("round-trips a local recovery-key-wrapped envelope", async () => {
    const dek = generateDEK();
    const canonical = generateRecoveryKey();
    const envelope = await wrapDEKWithRecovery(canonical, dek);
    const opened = await unwrapDEKWithRecovery(canonical, envelope);
    expect(bytesToHex(opened)).toBe(bytesToHex(dek));
    // Can still unwrap after normalization (input with separators and lowercase)
    const formatted = formatRecoveryKey(canonical);
    const opened2 = await unwrapDEKWithRecovery(normalizeRecoveryKey(formatted.toLowerCase()), envelope);
    expect(bytesToHex(opened2)).toBe(bytesToHex(dek));
    await expect(unwrapDEKWithRecovery("AAAAAAAAAAAAAAAAAAAAAAAAAAA", envelope)).rejects.toThrow();
  });

  it("matches the Go RecoveryKeyHash shape (sha256 hex)", async () => {
    const canonical = generateRecoveryKey();
    const hash = await recoveryKeyHash(canonical);
    expect(hash).toMatch(/^[0-9a-f]{64}$/);
    // Hash unchanged after normalization
    expect(await recoveryKeyHash(formatRecoveryKey(canonical))).toBe(hash);
    expect(await recoveryKeyHash(canonical.toLowerCase())).toBe(hash);
  });

  it("formats recovery keys as 32 base32 characters in groups of four", () => {
    const canonical = generateRecoveryKey();
    expect(canonical).toMatch(/^[A-Z2-7]{32}$/);
    const formatted = formatRecoveryKey(canonical);
    expect(formatted).toMatch(/^([A-Z2-7]{4}-){7}[A-Z2-7]{4}$/);
    expect(normalizeRecoveryKey(formatted)).toBe(canonical);
  });
});

describe("sync object encryption and AAD binding", () => {
  it("round-trips seal/open and binds AAD to the id and kind", async () => {
    const dek = generateDEK();
    const plaintext = utf8Bytes('{"id":"a1","name":"web-01"}');
    const blob = await sealSyncObject(dek, plaintext, "a1", "asset");
    const opened = await openSyncObject(dek, blob, "a1", "asset");
    expect(new TextDecoder().decode(opened)).toBe(new TextDecoder().decode(plaintext));

    // Changing the id fails to open
    await expect(openSyncObject(dek, blob, "a2", "asset")).rejects.toThrow();
    // Changing the kind fails to open
    await expect(openSyncObject(dek, blob, "a1", "group")).rejects.toThrow();
    // Changing the key fails to open
    await expect(openSyncObject(generateDEK(), blob, "a1", "asset")).rejects.toThrow();
    // Tampered ciphertext fails to open
    const tampered = blob.slice();
    tampered[tampered.length - 1] ^= 1;
    await expect(openSyncObject(dek, tampered, "a1", "asset")).rejects.toThrow();
  });

  it("decrypts tryOpenSyncObject by trying each kind", async () => {
    const dek = generateDEK();
    const plaintext = utf8Bytes('{"targetKind":"asset","deletedAt":1}');
    const blob = await sealSyncObject(dek, plaintext, "t1", "tombstone");
    const opened = await tryOpenSyncObject(dek, blob, "t1");
    expect(opened.kind).toBe("tombstone");
    expect(new TextDecoder().decode(opened.plaintext)).toBe(new TextDecoder().decode(plaintext));
    await expect(tryOpenSyncObject(generateDEK(), blob, "t1")).rejects.toThrow();
  });

  it("returns a sha256 hex objectPayloadHash", async () => {
    const hash = await objectPayloadHash(utf8Bytes("abc"));
    expect(hash).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });
});

describe("base64 interoperability", () => {
  it("matches Go encoding/json []byte encoding with the padded standard alphabet", () => {
    const data = new Uint8Array([0x09, 0x54, 0xaa, 0x80, 0xc4, 0xe4, 0x41, 0xfe]);
    expect(bytesToBase64(data)).toBe("CVSqgMTkQf4=");
    expect(base64ToBytes("CVSqgMTkQf4=")).toEqual(data);
    // Envelopes from the Go fixture decode directly
    expect(base64ToBytes(GO_FIXTURE.kdfSalt).length).toBe(16);
    expect(base64ToBytes(GO_FIXTURE.dekEnvelope).length).toBe(60);
  });
});
