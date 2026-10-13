// Account DEK envelope and sync object encryption/decryption, interoperating with internal/vault (userdek.go) and internal/sync (object.go).
// DEK plaintext exists only in caller memory: never persisted, never uploaded; the server stores only envelopes and ciphertext.

import { argon2idKey } from "./argon2";

export { bytesToBase64 } from "../../ui/base64";

const DEK_LENGTH = 32;
const NONCE_LENGTH = 12;
const TAG_LENGTH = 16;

const userDEKAAD = "nexterm/go/user-dek/v1";
const userDEKRecoveryAAD = "nexterm/go/user-dek-recovery/v1";
const objectAADPrefix = "nexterm/go/sync-object/v1";

// Same enum order as objectKinds in Go internal/sync/object.go (AAD binds kind; a wrong kind never decrypts).
export const SYNC_OBJECT_KINDS = ["group", "asset", "credential", "snippet", "tombstone", "transcript", "known_host", "ai_profile", "ai_permission", "ai_grant", "preference"] as const;
export type SyncObjectKind = (typeof SYNC_OBJECT_KINDS)[number];

// Go vault.userDEKKDFDefaults: the JSON form of Time/MemoryKiB/Threads is {"t":3,"m":65536,"p":4}.
const KDF_DEFAULTS = { t: 3, m: 65536, p: 4 };

const KDF_LIMITS = { maxTime: 16, maxMemoryKiB: 2 * 1024 * 1024, maxThreads: 16, minMemoryKiB: 8 * 1024 };

const RECOVERY_KEY_BYTES = 20;
const B32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export class CryptoError extends Error {
  constructor(
    readonly code: "crypto" | "decrypt" | "bad_param",
    message: string,
  ) {
    super(message);
    this.name = "CryptoError";
  }
}

function requireSubtle(): SubtleCrypto {
  if (typeof crypto === "undefined" || !crypto.subtle) {
    throw new CryptoError("crypto", "当前环境不支持 Web Crypto（需要 HTTPS 或 localhost）");
  }
  return crypto.subtle;
}

export function randomBytes(length: number): Uint8Array {
  const out = new Uint8Array(length);
  crypto.getRandomValues(out);
  return out;
}

export function base64ToBytes(text: string): Uint8Array {
  const binary = atob(text);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

export function bytesToHex(data: Uint8Array): string {
  return [...data].map((b) => b.toString(16).padStart(2, "0")).join("");
}

export function utf8Bytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

async function importAesKey(key: Uint8Array, usage: "encrypt" | "decrypt"): Promise<CryptoKey> {
  return requireSubtle().importKey("raw", key as unknown as BufferSource, { name: "AES-GCM" }, false, [usage]);
}

// seal outputs nonce(12B) || ciphertext || tag(16B), matching Go vault.seal's append(nonce, ciphertext...).
export async function aesGcmSeal(key: Uint8Array, plaintext: Uint8Array, aad: Uint8Array): Promise<Uint8Array> {
  const nonce = randomBytes(NONCE_LENGTH);
  const subtle = requireSubtle();
  const cryptoKey = await importAesKey(key, "encrypt");
  const ct = new Uint8Array(
    await subtle.encrypt({ name: "AES-GCM", iv: nonce as unknown as BufferSource, additionalData: aad as unknown as BufferSource }, cryptoKey, plaintext as unknown as BufferSource),
  );
  const out = new Uint8Array(NONCE_LENGTH + ct.length);
  out.set(nonce);
  out.set(ct, NONCE_LENGTH);
  return out;
}

export async function aesGcmOpen(key: Uint8Array, blob: Uint8Array, aad: Uint8Array): Promise<Uint8Array> {
  if (blob.length <= NONCE_LENGTH) {
    throw new CryptoError("decrypt", "解密失败：加密数据格式无效");
  }
  const subtle = requireSubtle();
  const cryptoKey = await importAesKey(key, "decrypt");
  try {
    const pt = await subtle.decrypt(
      { name: "AES-GCM", iv: blob.subarray(0, NONCE_LENGTH) as unknown as BufferSource, additionalData: aad as unknown as BufferSource },
      cryptoKey,
      blob.subarray(NONCE_LENGTH) as unknown as BufferSource,
    );
    return new Uint8Array(pt);
  } catch {
    throw new CryptoError("decrypt", "凭据解密失败（密钥不匹配或数据损坏）");
  }
}

export async function sha256Hex(data: Uint8Array): Promise<string> {
  const digest = await requireSubtle().digest("SHA-256", data as unknown as BufferSource);
  return bytesToHex(new Uint8Array(digest));
}

// ---- KDF params (JSON counterpart of Go vault.UserDEKKDFParams) ----

export interface KdfParams {
  t: number;
  m: number;
  p: number;
}

export function parseKdfParams(raw: string): KdfParams {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new CryptoError("bad_param", "参数错误：密钥派生参数格式无效");
  }
  const params = parsed as Partial<KdfParams>;
  if (
    typeof params.t !== "number" || typeof params.m !== "number" || typeof params.p !== "number" ||
    params.t === 0 || params.t > KDF_LIMITS.maxTime ||
    params.m < KDF_LIMITS.minMemoryKiB || params.m > KDF_LIMITS.maxMemoryKiB ||
    params.p === 0 || params.p > KDF_LIMITS.maxThreads
  ) {
    throw new CryptoError("bad_param", "参数错误：密钥派生参数超出允许范围");
  }
  return { t: params.t, m: params.m, p: params.p };
}

async function deriveUserKEK(password: string, salt: Uint8Array, params: KdfParams): Promise<Uint8Array> {
  if (salt.length < 16) {
    throw new CryptoError("crypto", "加密失败：盐值长度不足");
  }
  return argon2idKey(utf8Bytes(password), salt, params.t, params.m, params.p, DEK_LENGTH);
}

// ---- DEK envelope (counterpart of Go vault.WrapUserDEK/UnwrapUserDEK) ----

export interface DekEnvelopes {
  dekEnvelope: Uint8Array;
  kdfSalt: Uint8Array;
  kdfParams: string;
  recoveryEnvelope: Uint8Array;
  recoveryHash: string;
}

export function generateDEK(): Uint8Array {
  return randomBytes(DEK_LENGTH);
}

export async function wrapDEKWithPassword(password: string, dek: Uint8Array): Promise<{ envelope: Uint8Array; salt: Uint8Array; params: string }> {
  if (dek.length !== DEK_LENGTH) {
    throw new CryptoError("crypto", "加密失败：数据密钥长度无效");
  }
  const salt = randomBytes(16);
  const kek = await deriveUserKEK(password, salt, KDF_DEFAULTS);
  const envelope = await aesGcmSeal(kek, dek, utf8Bytes(userDEKAAD));
  return { envelope, salt, params: JSON.stringify(KDF_DEFAULTS) };
}

export async function unwrapDEKWithPassword(password: string, envelope: Uint8Array, salt: Uint8Array, paramsRaw: string): Promise<Uint8Array> {
  const params = parseKdfParams(paramsRaw);
  const kek = await deriveUserKEK(password, salt, params);
  if (envelope.length !== NONCE_LENGTH + DEK_LENGTH + TAG_LENGTH) {
    throw new CryptoError("decrypt", "解密失败：数据密钥格式无效");
  }
  const dek = await aesGcmOpen(kek, envelope, utf8Bytes(userDEKAAD));
  if (dek.length !== DEK_LENGTH) {
    throw new CryptoError("decrypt", "解密失败：数据密钥长度无效");
  }
  return dek;
}

// Recovery key KEK = the 20 bytes from base32 decoding, right-padded with 12 zeros to 32 bytes; on decode failure, the canonical string's UTF-8 bytes right-padded with zeros.
// Aligned with Go vault.recoveryKeyMaterial + copy(kek.bytes[:], key).
function recoveryKeyMaterial(canonical: string): Uint8Array {
  const key = new Uint8Array(DEK_LENGTH);
  const decoded = base32Decode(canonical);
  if (decoded !== null && decoded.length === RECOVERY_KEY_BYTES) {
    key.set(decoded);
    return key;
  }
  const raw = utf8Bytes(canonical);
  key.set(raw.subarray(0, Math.min(raw.length, DEK_LENGTH)));
  return key;
}

export async function wrapDEKWithRecovery(canonicalRecoveryKey: string, dek: Uint8Array): Promise<Uint8Array> {
  if (dek.length !== DEK_LENGTH) {
    throw new CryptoError("crypto", "加密失败：数据密钥长度无效");
  }
  const kek = recoveryKeyMaterial(canonicalRecoveryKey);
  return aesGcmSeal(kek, dek, utf8Bytes(userDEKRecoveryAAD));
}

export async function unwrapDEKWithRecovery(canonicalRecoveryKey: string, envelope: Uint8Array): Promise<Uint8Array> {
  if (envelope.length !== NONCE_LENGTH + DEK_LENGTH + TAG_LENGTH) {
    throw new CryptoError("decrypt", "解密失败：恢复数据格式无效");
  }
  const kek = recoveryKeyMaterial(canonicalRecoveryKey);
  const dek = await aesGcmOpen(kek, envelope, utf8Bytes(userDEKRecoveryAAD));
  if (dek.length !== DEK_LENGTH) {
    throw new CryptoError("decrypt", "解密失败：数据密钥长度无效");
  }
  return dek;
}

// ---- Recovery key (counterpart of Go vault.GenerateRecoveryKey/FormatRecoveryKey/NormalizeRecoveryKey/RecoveryKeyHash) ----

export function generateRecoveryKey(): string {
  return base32Encode(randomBytes(RECOVERY_KEY_BYTES));
}

export function formatRecoveryKey(canonical: string): string {
  const normalized = normalizeRecoveryKey(canonical);
  const groups: string[] = [];
  for (let i = 0; i + 4 <= normalized.length; i += 4) {
    groups.push(normalized.slice(i, i + 4));
  }
  return groups.join("-");
}

export function normalizeRecoveryKey(input: string): string {
  return input.replace(/[- \t]/g, "").toUpperCase();
}

export async function recoveryKeyHash(canonical: string): Promise<string> {
  return sha256Hex(utf8Bytes(normalizeRecoveryKey(canonical)));
}

// Standard base32 alphabet, no padding, matching Go base32.StdEncoding.WithPadding(NoPadding).
function base32Encode(data: Uint8Array): string {
  let bits = 0;
  let value = 0;
  let out = "";
  for (const byte of data) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += B32_ALPHABET[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) {
    out += B32_ALPHABET[(value << (5 - bits)) & 31];
  }
  return out;
}

function base32Decode(text: string): Uint8Array | null {
  let bits = 0;
  let value = 0;
  const out: number[] = [];
  for (const ch of text) {
    const idx = B32_ALPHABET.indexOf(ch);
    if (idx < 0) return null;
    value = (value << 5) | idx;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return new Uint8Array(out);
}

// ---- Sync objects (counterpart of sealObject/openObject in internal/sync/object.go) ----

function objectAAD(id: string, kind: string): Uint8Array {
  return utf8Bytes(`${objectAADPrefix}\x00${id}\x00${kind}`);
}

export async function sealSyncObject(dek: Uint8Array, plaintext: Uint8Array, id: string, kind: SyncObjectKind): Promise<Uint8Array> {
  if (dek.length !== DEK_LENGTH) {
    throw new CryptoError("crypto", "加密失败：数据密钥长度无效");
  }
  return aesGcmSeal(dek, plaintext, objectAAD(id, kind));
}

export async function openSyncObject(dek: Uint8Array, blob: Uint8Array, id: string, kind: SyncObjectKind): Promise<Uint8Array> {
  return aesGcmOpen(dek, blob, objectAAD(id, kind));
}

// tryOpenSyncObject tries decryption across the fixed kind enum; if all fail, the key does not match or the data was tampered with.
export async function tryOpenSyncObject(
  dek: Uint8Array,
  blob: Uint8Array,
  id: string,
): Promise<{ kind: SyncObjectKind; plaintext: Uint8Array }> {
  for (const kind of SYNC_OBJECT_KINDS) {
    try {
      const plaintext = await openSyncObject(dek, blob, id, kind);
      return { kind, plaintext };
    } catch {
      // Try the next kind
    }
  }
  throw new CryptoError("decrypt", "凭据解密失败（密钥不匹配或数据损坏）");
}

export async function objectPayloadHash(plaintext: Uint8Array): Promise<string> {
  return sha256Hex(plaintext);
}
