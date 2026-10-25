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
    throw new CryptoError("decrypt", "解密失败（密钥不匹配或数据损坏）");
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

// Compressed transcripts (internal/sync/object_compress.go). Both labels seal "NXZ1" | uint64 BE decoded size | stream.
// "transcript.gzip" carries a gzip stream of the logical JSON. "transcript.zpack" carries a zstd frame of a pack body:
// the JSON skeleton with every chunk "data" string emptied, followed by the raw chunk bytes; refilling each string
// with the base64 of its bytes rebuilds the exact JSON, so payload hashes match the Go side. The legacy
// "transcript.zstd" label is deliberately not read here; such an object fails as undecryptable and is quarantined
// visibly.
export const COMPRESSED_TRANSCRIPT_LABEL = "transcript.gzip";
export const PACKED_TRANSCRIPT_LABEL = "transcript.zpack";
// Must equal maxSyncObjectBytes in internal/sync.
export const MAX_COMPRESSED_DECODED_BYTES = 96 << 20;
const COMPRESSED_MAGIC = "NXZ1";
const COMPRESSED_HEADER_BYTES = 12;
const PACK_MAGIC = "NXP1";
// Matches the Go decoder's window cap; fzstd allocates the declared window up front, so it is checked first.
const MAX_ZSTD_WINDOW_BYTES = 64 << 20;
const PACK_SLOT = utf8Bytes(`"data":"`);
const QUOTE = 0x22;

const badCompressed = () => new CryptoError("decrypt", "同步对象压缩数据不合法");

function compressedDeclaredSize(packed: Uint8Array, maxDecoded: number): number {
  if (packed.length <= COMPRESSED_HEADER_BYTES) throw badCompressed();
  for (let i = 0; i < COMPRESSED_MAGIC.length; i++) {
    if (packed[i] !== COMPRESSED_MAGIC.charCodeAt(i)) throw badCompressed();
  }
  const view = new DataView(packed.buffer, packed.byteOffset, packed.byteLength);
  const declared = view.getBigUint64(COMPRESSED_MAGIC.length, false);
  if (declared === 0n || declared > BigInt(maxDecoded)) throw badCompressed();
  return Number(declared);
}

export async function inflateTranscriptPlaintext(packed: Uint8Array, maxDecoded = MAX_COMPRESSED_DECODED_BYTES): Promise<Uint8Array> {
  const bad = badCompressed;
  const declared = compressedDeclaredSize(packed, maxDecoded);
  if (typeof DecompressionStream === "undefined") {
    throw new CryptoError("crypto", "当前浏览器不支持解压同步对象");
  }
  const inflater = new DecompressionStream("gzip");
  const writer = inflater.writable.getWriter();
  // A malformed stream rejects both sides; the read loop below reports it, so the write side is only drained.
  void writer.write(packed.subarray(COMPRESSED_HEADER_BYTES) as unknown as BufferSource).catch(() => undefined);
  void writer.close().catch(() => undefined);
  const reader = inflater.readable.getReader();
  const chunks: Uint8Array[] = [];
  let filled = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      // Memory is bounded by the declared size: a bomb or a lying header is cut off at the first excess byte.
      if (filled + value.length > declared) throw bad();
      chunks.push(value);
      filled += value.length;
    }
  } catch (error) {
    await reader.cancel().catch(() => undefined);
    throw error instanceof CryptoError ? error : bad();
  }
  if (filled !== declared) throw bad();
  const out = new Uint8Array(declared);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

// zstdWindowBytes reads the window a zstd frame header asks the decoder to allocate (RFC 8878 section 3.1.1.1).
function zstdWindowBytes(frame: Uint8Array): number {
  if (frame.length < 6 || frame[0] !== 0x28 || frame[1] !== 0xb5 || frame[2] !== 0x2f || frame[3] !== 0xfd) throw badCompressed();
  const descriptor = frame[4];
  const singleSegment = (descriptor & 0x20) !== 0;
  if (!singleSegment) {
    const windowDescriptor = frame[5];
    const base = 2 ** (10 + (windowDescriptor >> 3));
    return base + (base / 8) * (windowDescriptor & 7);
  }
  const dictionaryBytes = [0, 1, 2, 4][descriptor & 3];
  const sizeBytes = [1, 2, 4, 8][descriptor >> 6];
  const at = 5 + dictionaryBytes;
  if (frame.length < at + sizeBytes) throw badCompressed();
  let size = 0;
  for (let i = sizeBytes - 1; i >= 0; i--) size = size * 256 + frame[at + i];
  return sizeBytes === 2 ? size + 256 : size;
}

async function zstdDecodeExact(frame: Uint8Array, declared: number): Promise<Uint8Array> {
  // The window is bounded by the declared output as well, so a frame cannot make the decoder allocate more than the
  // body it claims to produce.
  if (zstdWindowBytes(frame) > Math.min(MAX_ZSTD_WINDOW_BYTES, Math.max(declared, 1 << 10))) throw badCompressed();
  const { Decompress } = await import("fzstd");
  const out = new Uint8Array(declared);
  let filled = 0;
  const stream = new Decompress((chunk) => {
    if (filled + chunk.length > declared) throw badCompressed();
    out.set(chunk, filled);
    filled += chunk.length;
  });
  try {
    stream.push(frame, true);
  } catch (error) {
    throw error instanceof CryptoError ? error : badCompressed();
  }
  if (filled !== declared) throw badCompressed();
  return out;
}

function indexOfBytes(haystack: Uint8Array, needle: Uint8Array, from: number): number {
  const last = haystack.length - needle.length;
  outer: for (let i = from; i <= last; i++) {
    for (let j = 0; j < needle.length; j++) {
      if (haystack[i + j] !== needle[j]) continue outer;
    }
    return i;
  }
  return -1;
}

const BASE64_ALPHABET = utf8Bytes("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/");

function writeBase64(out: Uint8Array, at: number, data: Uint8Array): number {
  let i = 0;
  for (; i + 2 < data.length; i += 3) {
    const n = (data[i] << 16) | (data[i + 1] << 8) | data[i + 2];
    out[at++] = BASE64_ALPHABET[n >> 18];
    out[at++] = BASE64_ALPHABET[(n >> 12) & 63];
    out[at++] = BASE64_ALPHABET[(n >> 6) & 63];
    out[at++] = BASE64_ALPHABET[n & 63];
  }
  const remaining = data.length - i;
  if (remaining > 0) {
    const n = (data[i] << 16) | (remaining === 2 ? data[i + 1] << 8 : 0);
    out[at++] = BASE64_ALPHABET[n >> 18];
    out[at++] = BASE64_ALPHABET[(n >> 12) & 63];
    out[at++] = remaining === 2 ? BASE64_ALPHABET[(n >> 6) & 63] : 0x3d;
    out[at++] = 0x3d;
  }
  return at;
}

// unpackTranscriptBody mirrors unpackTranscriptBody in internal/sync/object_compress.go.
export function unpackTranscriptBody(body: Uint8Array, maxDecoded = MAX_COMPRESSED_DECODED_BYTES): Uint8Array {
  const bad = badCompressed;
  for (let i = 0; i < PACK_MAGIC.length; i++) {
    if (body[i] !== PACK_MAGIC.charCodeAt(i)) throw bad();
  }
  let at = PACK_MAGIC.length;
  const readLength = (): number => {
    let value = 0;
    for (let shift = 0; shift < 35; shift += 7) {
      if (at >= body.length) throw bad();
      const byte = body[at++];
      value += (byte & 0x7f) * 2 ** shift;
      if (byte < 0x80) {
        if (value > body.length) throw bad();
        return value;
      }
    }
    throw bad();
  };
  const skeletonLength = readLength();
  if (skeletonLength > body.length - at) throw bad();
  const skeleton = body.subarray(at, at + skeletonLength);
  at += skeletonLength;
  const slots = readLength();
  if (slots === 0 || slots > body.length - at) throw bad();
  const lengths: number[] = [];
  let total = skeleton.length;
  let dataBytes = 0;
  for (let i = 0; i < slots; i++) {
    const length = readLength();
    lengths.push(length);
    dataBytes += length;
    total += Math.ceil(length / 3) * 4;
    if (dataBytes > body.length - at || total > maxDecoded) throw bad();
  }
  if (dataBytes !== body.length - at) throw bad();
  const out = new Uint8Array(total);
  let written = 0;
  let from = 0;
  for (const length of lengths) {
    const slot = indexOfBytes(skeleton, PACK_SLOT, from);
    const open = slot + PACK_SLOT.length;
    if (slot < 0 || open >= skeleton.length || skeleton[open] !== QUOTE) throw bad();
    out.set(skeleton.subarray(from, open), written);
    written = writeBase64(out, written + open - from, body.subarray(at, at + length));
    at += length;
    from = open;
  }
  if (indexOfBytes(skeleton, PACK_SLOT, from) >= 0) throw bad();
  out.set(skeleton.subarray(from), written);
  return out;
}

export async function unpackTranscriptPlaintext(packed: Uint8Array, maxDecoded = MAX_COMPRESSED_DECODED_BYTES): Promise<Uint8Array> {
  const declared = compressedDeclaredSize(packed, maxDecoded);
  const body = await zstdDecodeExact(packed.subarray(COMPRESSED_HEADER_BYTES), declared);
  return unpackTranscriptBody(body, maxDecoded);
}

// Below this size the gzip framing is not worth a format change (compressMinBytes in Go).
const COMPRESS_MIN_BYTES = 1 << 10;

async function gzipTranscriptPlaintext(plaintext: Uint8Array): Promise<Uint8Array | null> {
  if (typeof CompressionStream === "undefined") {
    throw new CryptoError("crypto", "当前浏览器不支持压缩同步对象");
  }
  const deflater = new CompressionStream("gzip");
  const writer = deflater.writable.getWriter();
  void writer.write(plaintext as unknown as BufferSource).catch(() => undefined);
  void writer.close().catch(() => undefined);
  const reader = deflater.readable.getReader();
  const parts: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
    size += value.length;
  }
  const gzipped = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) {
    gzipped.set(part, offset);
    offset += part.length;
  }
  if (COMPRESSED_HEADER_BYTES + gzipped.length >= plaintext.length) return null;
  const packed = new Uint8Array(COMPRESSED_HEADER_BYTES + gzipped.length);
  for (let i = 0; i < COMPRESSED_MAGIC.length; i++) packed[i] = COMPRESSED_MAGIC.charCodeAt(i);
  new DataView(packed.buffer).setBigUint64(COMPRESSED_MAGIC.length, BigInt(plaintext.length), false);
  packed.set(gzipped, COMPRESSED_HEADER_BYTES);
  return packed;
}

// sealSyncObjectWire is the upload writer: transcripts are always gzip-compressed (the browser has no zstd encoder for
// the zpack layout); only tiny or incompressible transcripts and other kinds keep their plain label.
export async function sealSyncObjectWire(dek: Uint8Array, plaintext: Uint8Array, id: string, kind: SyncObjectKind): Promise<Uint8Array> {
  if (kind === "transcript" && plaintext.length >= COMPRESS_MIN_BYTES && plaintext.length <= MAX_COMPRESSED_DECODED_BYTES) {
    const packed = await gzipTranscriptPlaintext(plaintext);
    if (packed) {
      if (dek.length !== DEK_LENGTH) {
        throw new CryptoError("crypto", "加密失败：数据密钥长度无效");
      }
      return aesGcmSeal(dek, packed, objectAAD(id, COMPRESSED_TRANSCRIPT_LABEL));
    }
  }
  return sealSyncObject(dek, plaintext, id, kind);
}

async function openCompressedLabel(dek: Uint8Array, blob: Uint8Array, id: string, label: string): Promise<Uint8Array | null> {
  try {
    return await aesGcmOpen(dek, blob, objectAAD(id, label));
  } catch (error) {
    if (!(error instanceof CryptoError && error.code === "decrypt")) throw error;
    return null;
  }
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
    } catch (error) {
      if (!(error instanceof CryptoError && error.code === "decrypt")) throw error;
    }
  }
  const packed = await openCompressedLabel(dek, blob, id, PACKED_TRANSCRIPT_LABEL);
  if (packed) {
    return { kind: "transcript", plaintext: await unpackTranscriptPlaintext(packed) };
  }
  const gzipped = await openCompressedLabel(dek, blob, id, COMPRESSED_TRANSCRIPT_LABEL);
  if (gzipped) {
    return { kind: "transcript", plaintext: await inflateTranscriptPlaintext(gzipped) };
  }
  throw new CryptoError("decrypt", "解密失败（密钥不匹配或数据损坏）");
}

export async function objectPayloadHash(plaintext: Uint8Array): Promise<string> {
  return sha256Hex(plaintext);
}
