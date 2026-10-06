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

// 真实 Go 端 vault.GenerateUserDEKEnvelopes 产物(scripts/syncv2-helper gen-dek,一次性生成)。
const GO_FIXTURE = {
  password: "test-password-123",
  dekHex: "c26ae1b3822f6f6dc4da86e69b7d5e8fde0b0fa29ddd6481716db07fcce032ad",
  dekEnvelope: "09TqgMTkQf7UvK2nzp5u3S48ggs0d/yHM4xh5rC5OYbGkUJfNEnYnzcNgOsSRkMAK8MoUNmkU0GKfMwl",
  kdfSalt: "0lDbbAdGDsSjmcAs/vsNtQ==",
  kdfParams: '{"t":3,"m":65536,"p":4}',
  recoveryEnvelope: "PR04goBuVFxFwswIg+lsgZaQySPbm2/ft+4gRl6YFvgUW4JQhoaXLMscw+yOrXhX4+mkcKKh5EnZQJwK",
  recoveryHash: "bf19cc2446a331d83b984666f621e480196ba373402a91e4adad34ea7ac0df82",
};

describe("DEK 信封与 Go vault 互通", () => {
  it("解包 Go 端密码信封(生产参数 argon2id)", async () => {
    const dek = await unwrapDEKWithPassword(
      GO_FIXTURE.password,
      base64ToBytes(GO_FIXTURE.dekEnvelope),
      base64ToBytes(GO_FIXTURE.kdfSalt),
      GO_FIXTURE.kdfParams,
    );
    expect(bytesToHex(dek)).toBe(GO_FIXTURE.dekHex);
  }, 60_000);

  it("本端信封往返: 密码包裹 → 解包", async () => {
    const dek = generateDEK();
    const { envelope, salt, params } = await wrapDEKWithPassword("correct horse battery staple", dek);
    expect(params).toBe('{"t":3,"m":65536,"p":4}');
    const opened = await unwrapDEKWithPassword("correct horse battery staple", envelope, salt, params);
    expect(bytesToHex(opened)).toBe(bytesToHex(dek));
    await expect(unwrapDEKWithPassword("wrong password", envelope, salt, params)).rejects.toThrow();
  }, 60_000);

  it("本端信封往返: 恢复密钥包裹 → 解包", async () => {
    const dek = generateDEK();
    const canonical = generateRecoveryKey();
    const envelope = await wrapDEKWithRecovery(canonical, dek);
    const opened = await unwrapDEKWithRecovery(canonical, envelope);
    expect(bytesToHex(opened)).toBe(bytesToHex(dek));
    // 规范化(带分隔符与小写输入)后仍可解包
    const formatted = formatRecoveryKey(canonical);
    const opened2 = await unwrapDEKWithRecovery(normalizeRecoveryKey(formatted.toLowerCase()), envelope);
    expect(bytesToHex(opened2)).toBe(bytesToHex(dek));
    await expect(unwrapDEKWithRecovery("AAAAAAAAAAAAAAAAAAAAAAAAAAA", envelope)).rejects.toThrow();
  });

  it("恢复密钥散列与 Go RecoveryKeyHash 形状一致(sha256 hex)", async () => {
    const canonical = generateRecoveryKey();
    const hash = await recoveryKeyHash(canonical);
    expect(hash).toMatch(/^[0-9a-f]{64}$/);
    // 规范化后散列不变
    expect(await recoveryKeyHash(formatRecoveryKey(canonical))).toBe(hash);
    expect(await recoveryKeyHash(canonical.toLowerCase())).toBe(hash);
  });

  it("恢复密钥格式: 32 字符 base32,4 字符分组", () => {
    const canonical = generateRecoveryKey();
    expect(canonical).toMatch(/^[A-Z2-7]{32}$/);
    const formatted = formatRecoveryKey(canonical);
    expect(formatted).toMatch(/^([A-Z2-7]{4}-){7}[A-Z2-7]{4}$/);
    expect(normalizeRecoveryKey(formatted)).toBe(canonical);
  });
});

describe("同步对象加解密与 AAD 绑定", () => {
  it("seal/open 往返,且 AAD 绑定 id 与 kind", async () => {
    const dek = generateDEK();
    const plaintext = utf8Bytes('{"id":"a1","name":"web-01"}');
    const blob = await sealSyncObject(dek, plaintext, "a1", "asset");
    const opened = await openSyncObject(dek, blob, "a1", "asset");
    expect(new TextDecoder().decode(opened)).toBe(new TextDecoder().decode(plaintext));

    // 换 id 解不开
    await expect(openSyncObject(dek, blob, "a2", "asset")).rejects.toThrow();
    // 换 kind 解不开
    await expect(openSyncObject(dek, blob, "a1", "group")).rejects.toThrow();
    // 换密钥解不开
    await expect(openSyncObject(generateDEK(), blob, "a1", "asset")).rejects.toThrow();
    // 篡改密文解不开
    const tampered = blob.slice();
    tampered[tampered.length - 1] ^= 1;
    await expect(openSyncObject(dek, tampered, "a1", "asset")).rejects.toThrow();
  });

  it("tryOpenSyncObject 按 kind 枚举解密", async () => {
    const dek = generateDEK();
    const plaintext = utf8Bytes('{"targetKind":"asset","deletedAt":1}');
    const blob = await sealSyncObject(dek, plaintext, "t1", "tombstone");
    const opened = await tryOpenSyncObject(dek, blob, "t1");
    expect(opened.kind).toBe("tombstone");
    expect(new TextDecoder().decode(opened.plaintext)).toBe(new TextDecoder().decode(plaintext));
    await expect(tryOpenSyncObject(generateDEK(), blob, "t1")).rejects.toThrow();
  });

  it("objectPayloadHash 为 sha256 hex", async () => {
    const hash = await objectPayloadHash(utf8Bytes("abc"));
    expect(hash).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });
});

describe("base64 互操作", () => {
  it("与 Go encoding/json 的 []byte 编码一致(标准字母表带填充)", () => {
    const data = new Uint8Array([0x09, 0x54, 0xaa, 0x80, 0xc4, 0xe4, 0x41, 0xfe]);
    expect(bytesToBase64(data)).toBe("CVSqgMTkQf4=");
    expect(base64ToBytes("CVSqgMTkQf4=")).toEqual(data);
    // Go fixture 中的信封可直接解码
    expect(base64ToBytes(GO_FIXTURE.kdfSalt).length).toBe(16);
    expect(base64ToBytes(GO_FIXTURE.dekEnvelope).length).toBe(60);
  });
});
