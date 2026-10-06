// argon2id(RFC 9106, v=19) 纯 TS 实现,逐行对齐 golang.org/x/crypto/argon2 v0.57.0,
// 供浏览器端解包账号 DEK 信封;与 Go 端 vault.WrapUserDEK/UnwrapUserDEK 互通。

const BLOCK_QWORDS = 128;
const BLOCK_UINT32 = BLOCK_QWORDS * 2;
const SYNC_POINTS = 4;
const VERSION = 0x13;

const IV = new Uint32Array([
  0xf3bcc908, 0x6a09e667, 0x84caa73b, 0xbb67ae85, 0xfe94f82b, 0x3c6ef372, 0x5f1d36f1, 0xa54ff53a,
  0xade682d1, 0x510e527f, 0x2b3e6c1f, 0x9b05688c, 0xfb41bd6b, 0x1f83d9ab, 0x137e2179, 0x5be0cd19,
]);

const SIGMA = [
  [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
  [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
  [11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4],
  [7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8],
  [9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13],
  [2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9],
  [12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11],
  [13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10],
  [6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5],
  [10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0],
  [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
  [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
];

function rotr32(lo: number, hi: number): [number, number] {
  return [hi, lo];
}

function rotr24(lo: number, hi: number): [number, number] {
  return [((lo >>> 24) | (hi << 8)) >>> 0, ((hi >>> 24) | (lo << 8)) >>> 0];
}

function rotr16(lo: number, hi: number): [number, number] {
  return [((lo >>> 16) | (hi << 16)) >>> 0, ((hi >>> 16) | (lo << 16)) >>> 0];
}

function rotr63(lo: number, hi: number): [number, number] {
  return [((lo << 1) | (hi >>> 31)) >>> 0, ((hi << 1) | (lo >>> 31)) >>> 0];
}

function add64(alo: number, ahi: number, blo: number, bhi: number): [number, number] {
  const lo = (alo + blo) >>> 0;
  const hi = (ahi + bhi + (lo < alo >>> 0 ? 1 : 0)) >>> 0;
  return [lo, hi];
}

// (a*b) mod 2^64,a、b 均为 uint32。
function mul32to64(a: number, b: number): [number, number] {
  const a0 = a & 0xffff;
  const a1 = a >>> 16;
  const b0 = b & 0xffff;
  const b1 = b >>> 16;
  const s0 = a0 * b0;
  const s1 = a1 * b0 + a0 * b1;
  const s2 = a1 * b1;
  const mid = (s0 >>> 16) + (s1 & 0xffff);
  const lo = (((mid & 0xffff) << 16) | (s0 & 0xffff)) >>> 0;
  const hi = (s2 + Math.floor(s1 / 0x10000) + Math.floor(mid / 0x10000)) >>> 0;
  return [lo, hi];
}

// fBlaMka(x, y) = x + y + 2*lo32(x)*lo32(y) (mod 2^64)
function blamkaAdd(xlo: number, xhi: number, ylo: number, yhi: number): [number, number] {
  const [plo, phi] = mul32to64(xlo, ylo);
  const dlo = (plo << 1) >>> 0;
  const dhi = ((phi << 1) | (plo >>> 31)) >>> 0;
  const [slo, shi] = add64(xlo, xhi, ylo, yhi);
  return add64(slo, shi, dlo, dhi);
}

// BLAKE2b 原始哈希(1..64 字节输出),密钥为空。
export function blake2b(input: Uint8Array, outLen: number): Uint8Array {
  const h = new Uint32Array(16);
  for (let i = 0; i < 8; i++) {
    h[i * 2] = IV[i * 2];
    h[i * 2 + 1] = IV[i * 2 + 1];
  }
  h[0] ^= 0x01010000 ^ outLen;

  const v = new Uint32Array(32);
  const m = new Uint32Array(32);
  let tLo = 0;
  let tHi = 0;

  const compress = (block: Uint8Array, offset: number, byteCount: number, last: boolean): void => {
    for (let i = 0; i < 16; i++) {
      const lo = (block[offset + i * 8] | (block[offset + i * 8 + 1] << 8) | (block[offset + i * 8 + 2] << 16) | (block[offset + i * 8 + 3] << 24)) >>> 0;
      const hi = (block[offset + i * 8 + 4] | (block[offset + i * 8 + 5] << 8) | (block[offset + i * 8 + 6] << 16) | (block[offset + i * 8 + 7] << 24)) >>> 0;
      m[i * 2] = lo;
      m[i * 2 + 1] = hi;
    }
    for (let i = 0; i < 16; i++) v[i] = h[i];
    for (let i = 0; i < 16; i++) v[i + 16] = IV[i];

    // 计数器 = 已压缩的真实字节数(最后一块按实际长度计,不是填充后的 128)
    const newLo = (tLo + byteCount) >>> 0;
    if (newLo < tLo) tHi = (tHi + 1) >>> 0;
    tLo = newLo;
    v[24] ^= tLo;
    v[25] ^= tHi;
    if (last) {
      v[28] = ~v[28] >>> 0;
      v[29] = ~v[29] >>> 0;
    }

    const g = (a: number, b: number, c: number, d: number, xlo: number, xhi: number, ylo: number, yhi: number): void => {
      let r = add64(v[a * 2], v[a * 2 + 1], v[b * 2], v[b * 2 + 1]);
      r = add64(r[0], r[1], xlo, xhi);
      v[a * 2] = r[0];
      v[a * 2 + 1] = r[1];
      let q = rotr32(v[d * 2] ^ v[a * 2], v[d * 2 + 1] ^ v[a * 2 + 1]);
      v[d * 2] = q[0];
      v[d * 2 + 1] = q[1];
      r = add64(v[c * 2], v[c * 2 + 1], v[d * 2], v[d * 2 + 1]);
      v[c * 2] = r[0];
      v[c * 2 + 1] = r[1];
      q = rotr24(v[b * 2] ^ v[c * 2], v[b * 2 + 1] ^ v[c * 2 + 1]);
      v[b * 2] = q[0];
      v[b * 2 + 1] = q[1];
      r = add64(v[a * 2], v[a * 2 + 1], v[b * 2], v[b * 2 + 1]);
      r = add64(r[0], r[1], ylo, yhi);
      v[a * 2] = r[0];
      v[a * 2 + 1] = r[1];
      q = rotr16(v[d * 2] ^ v[a * 2], v[d * 2 + 1] ^ v[a * 2 + 1]);
      v[d * 2] = q[0];
      v[d * 2 + 1] = q[1];
      r = add64(v[c * 2], v[c * 2 + 1], v[d * 2], v[d * 2 + 1]);
      v[c * 2] = r[0];
      v[c * 2 + 1] = r[1];
      q = rotr63(v[b * 2] ^ v[c * 2], v[b * 2 + 1] ^ v[c * 2 + 1]);
      v[b * 2] = q[0];
      v[b * 2 + 1] = q[1];
    };

    for (let round = 0; round < 12; round++) {
      const s = SIGMA[round];
      g(0, 4, 8, 12, m[s[0] * 2], m[s[0] * 2 + 1], m[s[1] * 2], m[s[1] * 2 + 1]);
      g(1, 5, 9, 13, m[s[2] * 2], m[s[2] * 2 + 1], m[s[3] * 2], m[s[3] * 2 + 1]);
      g(2, 6, 10, 14, m[s[4] * 2], m[s[4] * 2 + 1], m[s[5] * 2], m[s[5] * 2 + 1]);
      g(3, 7, 11, 15, m[s[6] * 2], m[s[6] * 2 + 1], m[s[7] * 2], m[s[7] * 2 + 1]);
      g(0, 5, 10, 15, m[s[8] * 2], m[s[8] * 2 + 1], m[s[9] * 2], m[s[9] * 2 + 1]);
      g(1, 6, 11, 12, m[s[10] * 2], m[s[10] * 2 + 1], m[s[11] * 2], m[s[11] * 2 + 1]);
      g(2, 7, 8, 13, m[s[12] * 2], m[s[12] * 2 + 1], m[s[13] * 2], m[s[13] * 2 + 1]);
      g(3, 4, 9, 14, m[s[14] * 2], m[s[14] * 2 + 1], m[s[15] * 2], m[s[15] * 2 + 1]);
    }
    for (let i = 0; i < 8; i++) {
      h[i * 2] = (h[i * 2] ^ v[i * 2] ^ v[i * 2 + 16]) >>> 0;
      h[i * 2 + 1] = (h[i * 2 + 1] ^ v[i * 2 + 1] ^ v[i * 2 + 17]) >>> 0;
    }
  };

  const padded = new Uint8Array(Math.max(128, Math.ceil(input.length / 128) * 128));
  padded.set(input);
  const blocks = padded.length / 128;
  for (let i = 0; i < blocks; i++) {
    compress(padded, i * 128, Math.min(128, input.length - i * 128), i === blocks - 1);
  }

  const out = new Uint8Array(outLen);
  for (let i = 0; i < outLen; i++) {
    const qword = i >>> 3;
    const byteInQword = i & 7;
    const word = h[qword * 2 + (byteInQword >= 4 ? 1 : 0)];
    out[i] = (word >>> ((byteInQword % 4) * 8)) & 0xff;
  }
  return out;
}

// argon2 变长哈希 H':Go blake2bHash 的对应实现。
function blake2bHash(outLen: number, input: Uint8Array): Uint8Array {
  const lenPrefix = new Uint8Array(4);
  new DataView(lenPrefix.buffer).setUint32(0, outLen, true);
  const prefixed = new Uint8Array(4 + input.length);
  prefixed.set(lenPrefix);
  prefixed.set(input, 4);

  const out = new Uint8Array(outLen);
  if (outLen <= 64) {
    out.set(blake2b(prefixed, outLen));
    return out;
  }
  let v = blake2b(prefixed, 64);
  out.set(v.subarray(0, 32));
  let filled = 32;
  while (outLen - filled > 64) {
    v = blake2b(v, 64);
    out.set(v.subarray(0, 32), filled);
    filled += 32;
  }
  const lastLen = outLen - filled;
  out.set(blake2b(v, lastLen), filled);
  return out;
}

// blamka 轮:P 作用于 16 个 qword。paired=false 时第 k 个在 off+k*stride(行,stride=2);
// paired=true 时为成对列模式 (2i, 2i+1, 2i+16, 2i+17, …),第 k 个在 off+(k>>1)*stride+(k&1)*2。
function blamkaRound(t: Uint32Array, off: number, stride: number, paired: boolean): void {
  const o: number[] = new Array<number>(16);
  for (let k = 0; k < 16; k++) {
    o[k] = paired ? off + (k >> 1) * stride + (k & 1) * 2 : off + k * stride;
  }
  let v0l = t[o[0]], v0h = t[o[0] + 1];
  let v1l = t[o[1]], v1h = t[o[1] + 1];
  let v2l = t[o[2]], v2h = t[o[2] + 1];
  let v3l = t[o[3]], v3h = t[o[3] + 1];
  let v4l = t[o[4]], v4h = t[o[4] + 1];
  let v5l = t[o[5]], v5h = t[o[5] + 1];
  let v6l = t[o[6]], v6h = t[o[6] + 1];
  let v7l = t[o[7]], v7h = t[o[7] + 1];
  let v8l = t[o[8]], v8h = t[o[8] + 1];
  let v9l = t[o[9]], v9h = t[o[9] + 1];
  let v10l = t[o[10]], v10h = t[o[10] + 1];
  let v11l = t[o[11]], v11h = t[o[11] + 1];
  let v12l = t[o[12]], v12h = t[o[12] + 1];
  let v13l = t[o[13]], v13h = t[o[13] + 1];
  let v14l = t[o[14]], v14h = t[o[14] + 1];
  let v15l = t[o[15]], v15h = t[o[15] + 1];

  const gb = (
    alo: number, ahi: number, blo: number, bhi: number, clo: number, chi: number, dlo: number, dhi: number,
  ): [number, number, number, number, number, number, number, number] => {
    let r = blamkaAdd(alo, ahi, blo, bhi);
    alo = r[0]; ahi = r[1];
    let q = rotr32(dlo ^ alo, dhi ^ ahi);
    dlo = q[0]; dhi = q[1];
    r = blamkaAdd(clo, chi, dlo, dhi);
    clo = r[0]; chi = r[1];
    q = rotr24(blo ^ clo, bhi ^ chi);
    blo = q[0]; bhi = q[1];
    r = blamkaAdd(alo, ahi, blo, bhi);
    alo = r[0]; ahi = r[1];
    q = rotr16(dlo ^ alo, dhi ^ ahi);
    dlo = q[0]; dhi = q[1];
    r = blamkaAdd(clo, chi, dlo, dhi);
    clo = r[0]; chi = r[1];
    q = rotr63(blo ^ clo, bhi ^ chi);
    blo = q[0]; bhi = q[1];
    return [alo, ahi, blo, bhi, clo, chi, dlo, dhi];
  };

  let r: [number, number, number, number, number, number, number, number];
  r = gb(v0l, v0h, v4l, v4h, v8l, v8h, v12l, v12h);
  v0l = r[0]; v0h = r[1]; v4l = r[2]; v4h = r[3]; v8l = r[4]; v8h = r[5]; v12l = r[6]; v12h = r[7];
  r = gb(v1l, v1h, v5l, v5h, v9l, v9h, v13l, v13h);
  v1l = r[0]; v1h = r[1]; v5l = r[2]; v5h = r[3]; v9l = r[4]; v9h = r[5]; v13l = r[6]; v13h = r[7];
  r = gb(v2l, v2h, v6l, v6h, v10l, v10h, v14l, v14h);
  v2l = r[0]; v2h = r[1]; v6l = r[2]; v6h = r[3]; v10l = r[4]; v10h = r[5]; v14l = r[6]; v14h = r[7];
  r = gb(v3l, v3h, v7l, v7h, v11l, v11h, v15l, v15h);
  v3l = r[0]; v3h = r[1]; v7l = r[2]; v7h = r[3]; v11l = r[4]; v11h = r[5]; v15l = r[6]; v15h = r[7];

  r = gb(v0l, v0h, v5l, v5h, v10l, v10h, v15l, v15h);
  v0l = r[0]; v0h = r[1]; v5l = r[2]; v5h = r[3]; v10l = r[4]; v10h = r[5]; v15l = r[6]; v15h = r[7];
  r = gb(v1l, v1h, v6l, v6h, v11l, v11h, v12l, v12h);
  v1l = r[0]; v1h = r[1]; v6l = r[2]; v6h = r[3]; v11l = r[4]; v11h = r[5]; v12l = r[6]; v12h = r[7];
  r = gb(v2l, v2h, v7l, v7h, v8l, v8h, v13l, v13h);
  v2l = r[0]; v2h = r[1]; v7l = r[2]; v7h = r[3]; v8l = r[4]; v8h = r[5]; v13l = r[6]; v13h = r[7];
  r = gb(v3l, v3h, v4l, v4h, v9l, v9h, v14l, v14h);
  v3l = r[0]; v3h = r[1]; v4l = r[2]; v4h = r[3]; v9l = r[4]; v9h = r[5]; v14l = r[6]; v14h = r[7];

  t[o[0]] = v0l; t[o[0] + 1] = v0h;
  t[o[1]] = v1l; t[o[1] + 1] = v1h;
  t[o[2]] = v2l; t[o[2] + 1] = v2h;
  t[o[3]] = v3l; t[o[3] + 1] = v3h;
  t[o[4]] = v4l; t[o[4] + 1] = v4h;
  t[o[5]] = v5l; t[o[5] + 1] = v5h;
  t[o[6]] = v6l; t[o[6] + 1] = v6h;
  t[o[7]] = v7l; t[o[7] + 1] = v7h;
  t[o[8]] = v8l; t[o[8] + 1] = v8h;
  t[o[9]] = v9l; t[o[9] + 1] = v9h;
  t[o[10]] = v10l; t[o[10] + 1] = v10h;
  t[o[11]] = v11l; t[o[11] + 1] = v11h;
  t[o[12]] = v12l; t[o[12] + 1] = v12h;
  t[o[13]] = v13l; t[o[13] + 1] = v13h;
  t[o[14]] = v14l; t[o[14] + 1] = v14h;
  t[o[15]] = v15l; t[o[15] + 1] = v15h;
}

// fill_block:out = P(x^y)(^ 旧 out,xor 为 true 时);Go processBlockGeneric 的对应实现。
function fillBlock(out: Uint32Array, outOff: number, x: Uint32Array, xOff: number, y: Uint32Array, yOff: number, xor: boolean): void {
  const t = new Uint32Array(BLOCK_UINT32);
  for (let i = 0; i < BLOCK_UINT32; i++) {
    t[i] = (x[xOff + i] ^ y[yOff + i]) >>> 0;
  }
  for (let row = 0; row < 8; row++) {
    blamkaRound(t, row * 32, 2, false);
  }
  for (let col = 0; col < 8; col++) {
    blamkaRound(t, col * 4, 32, true);
  }
  for (let i = 0; i < BLOCK_UINT32; i++) {
    const value = (t[i] ^ x[xOff + i] ^ y[yOff + i]) >>> 0;
    if (xor) out[outOff + i] = (out[outOff + i] ^ value) >>> 0;
    else out[outOff + i] = value;
  }
}

function le32(value: number): Uint8Array {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, value >>> 0, true);
  return out;
}

function initHash(password: Uint8Array, salt: Uint8Array, time: number, memory: number, threads: number, keyLen: number): Uint8Array {
  const parts: Uint8Array[] = [
    le32(threads), le32(keyLen), le32(memory), le32(time), le32(VERSION), le32(2),
    le32(password.length), password,
    le32(salt.length), salt,
    le32(0), le32(0),
  ];
  let total = 0;
  for (const p of parts) total += p.length;
  const joined = new Uint8Array(total);
  let offset = 0;
  for (const p of parts) {
    joined.set(p, offset);
    offset += p.length;
  }
  return blake2b(joined, 64);
}

export function argon2idKey(
  password: Uint8Array,
  salt: Uint8Array,
  time: number,
  memoryKiB: number,
  threads: number,
  keyLen: number,
): Uint8Array {
  if (time < 1) throw new Error("argon2: 迭代次数过小");
  if (threads < 1) throw new Error("argon2: 并行度过低");

  const h0 = initHash(password, salt, time, memoryKiB, threads, keyLen);

  let memory = Math.floor(memoryKiB / (SYNC_POINTS * threads)) * (SYNC_POINTS * threads);
  if (memory < 2 * SYNC_POINTS * threads) memory = 2 * SYNC_POINTS * threads;

  const lanes = memory / threads;
  const segments = lanes / SYNC_POINTS;

  const B = new Uint32Array(memory * BLOCK_UINT32);
  const h0p = new Uint8Array(72);
  h0p.set(h0);
  const blockBytes = new Uint8Array(1024);
  for (let lane = 0; lane < threads; lane++) {
    const base = lane * lanes * BLOCK_UINT32;
    h0p.set(le32(lane), 68);
    h0p.set(le32(0), 64);
    blockBytes.set(blake2bHash(1024, h0p));
    for (let i = 0; i < BLOCK_QWORDS; i++) {
      B[base + i * 2] = (blockBytes[i * 8] | (blockBytes[i * 8 + 1] << 8) | (blockBytes[i * 8 + 2] << 16) | (blockBytes[i * 8 + 3] << 24)) >>> 0;
      B[base + i * 2 + 1] = (blockBytes[i * 8 + 4] | (blockBytes[i * 8 + 5] << 8) | (blockBytes[i * 8 + 6] << 16) | (blockBytes[i * 8 + 7] << 24)) >>> 0;
    }
    h0p.set(le32(1), 64);
    blockBytes.set(blake2bHash(1024, h0p));
    for (let i = 0; i < BLOCK_QWORDS; i++) {
      B[base + 256 + i * 2] = (blockBytes[i * 8] | (blockBytes[i * 8 + 1] << 8) | (blockBytes[i * 8 + 2] << 16) | (blockBytes[i * 8 + 3] << 24)) >>> 0;
      B[base + 256 + i * 2 + 1] = (blockBytes[i * 8 + 4] | (blockBytes[i * 8 + 5] << 8) | (blockBytes[i * 8 + 6] << 16) | (blockBytes[i * 8 + 7] << 24)) >>> 0;
    }
  }

  const addresses = new Uint32Array(BLOCK_UINT32);
  const input = new Uint32Array(BLOCK_UINT32);
  const zero = new Uint32Array(BLOCK_UINT32);

  const indexAlpha = (randLo: number, randHi: number, n: number, slice: number, lane: number, index: number): number => {
    let refLane = randHi % threads;
    if (n === 0 && slice === 0) refLane = lane;
    let m = 3 * segments;
    let s = ((slice + 1) % SYNC_POINTS) * segments;
    if (lane === refLane) m += index;
    if (n === 0) {
      m = slice * segments;
      s = 0;
      if (slice === 0 || lane === refLane) m += index;
    }
    if (index === 0 || lane === refLane) m--;
    // p = (rand&0xffffffff); p = p*p >> 32; p = p*m >> 32
    const sq = mul32to64(randLo, randLo);
    const p1 = sq[1];
    const prod = mul32to64(p1, m);
    const p2 = prod[1];
    return refLane * lanes + ((s + m - (p2 + 1)) % lanes);
  };

  for (let n = 0; n < time; n++) {
    for (let slice = 0; slice < SYNC_POINTS; slice++) {
      for (let lane = 0; lane < threads; lane++) {
        const dataIndependent = n === 0 && slice < SYNC_POINTS / 2;
        if (dataIndependent) {
          input.fill(0);
          input[0] = n;
          input[2] = lane;
          input[4] = slice;
          input[6] = memory;
          input[8] = time;
          input[10] = 2;
          input[12] = 0;
          input[13] = 0;
        }

        let index = 0;
        if (n === 0 && slice === 0) {
          index = 2;
          input[12] = (input[12] + 1) >>> 0;
          fillBlock(addresses, 0, input, 0, zero, 0, false);
          fillBlock(addresses, 0, addresses, 0, zero, 0, false);
        }

        let offset = lane * lanes + slice * segments + index;
        while (index < segments) {
          let prev = offset - 1;
          if (index === 0 && slice === 0) prev += lanes;
          let randLo: number;
          let randHi: number;
          if (dataIndependent) {
            if (index % BLOCK_QWORDS === 0) {
              input[12] = (input[12] + 1) >>> 0;
              fillBlock(addresses, 0, input, 0, zero, 0, false);
              fillBlock(addresses, 0, addresses, 0, zero, 0, false);
            }
            const word = (index % BLOCK_QWORDS) * 2;
            randLo = addresses[word];
            randHi = addresses[word + 1];
          } else {
            randLo = B[prev * BLOCK_UINT32];
            randHi = B[prev * BLOCK_UINT32 + 1];
          }
          const newOffset = indexAlpha(randLo, randHi, n, slice, lane, index);
          fillBlock(B, offset * BLOCK_UINT32, B, prev * BLOCK_UINT32, B, newOffset * BLOCK_UINT32, true);
          index += 1;
          offset += 1;
        }
      }
    }
  }

  const lastBlock = (lanes - 1) * BLOCK_UINT32;
  for (let lane = 0; lane < threads - 1; lane++) {
    const base = lane * lanes * BLOCK_UINT32 + lastBlock;
    const target = (memory - 1) * BLOCK_UINT32;
    for (let i = 0; i < BLOCK_UINT32; i++) {
      B[target + i] = (B[target + i] ^ B[base + i]) >>> 0;
    }
  }
  for (let i = 0; i < BLOCK_QWORDS; i++) {
    const base = (memory - 1) * BLOCK_UINT32 + i * 2;
    blockBytes[i * 8] = B[base] & 0xff;
    blockBytes[i * 8 + 1] = (B[base] >>> 8) & 0xff;
    blockBytes[i * 8 + 2] = (B[base] >>> 16) & 0xff;
    blockBytes[i * 8 + 3] = (B[base] >>> 24) & 0xff;
    blockBytes[i * 8 + 4] = B[base + 1] & 0xff;
    blockBytes[i * 8 + 5] = (B[base + 1] >>> 8) & 0xff;
    blockBytes[i * 8 + 6] = (B[base + 1] >>> 16) & 0xff;
    blockBytes[i * 8 + 7] = (B[base + 1] >>> 24) & 0xff;
  }
  return blake2bHash(keyLen, blockBytes);
}
