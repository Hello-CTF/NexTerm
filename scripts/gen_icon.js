import { deflateSync } from "node:zlib";
import { writeFileSync } from "node:fs";

const W = 1024;
const H = 1024;

function pixel(x, y) {
  const t = y / H;
  let r = Math.round(0x1e + (0x0d - 0x1e) * t);
  let g = Math.round(0x2a + (0x11 - 0x2a) * t);
  let b = Math.round(0x4a + (0x17 - 0x4a) * t);
  const m = 64;
  const inCorner =
    (x < m && y < m && (x - m) ** 2 + (y - m) ** 2 > m * m) ||
    (x > W - m && y < m && (x - (W - m)) ** 2 + (y - m) ** 2 > m * m) ||
    (x < m && y > H - m && (x - m) ** 2 + (y - (H - m)) ** 2 > m * m) ||
    (x > W - m && y > H - m && (x - (W - m)) ** 2 + (y - (H - m)) ** 2 > m * m);
  if (inCorner) return [0, 0, 0, 0];
  const cx = x - W / 2;
  const cy = y - H / 2;
  const half = 300;
  const stroke = 96;
  const inLeft = cx >= -half && cx <= -half + stroke && Math.abs(cy) <= half;
  const inRight = cx >= half - stroke && cx <= half && Math.abs(cy) <= half;
  const diagX = -half + ((cy + half) / (2 * half)) * (2 * half - stroke);
  const inDiag = Math.abs(cx - diagX) <= stroke / 2 + 20 && Math.abs(cy) <= half;
  if (inLeft || inRight || inDiag) {
    return [0xf0, 0xf4, 0xff, 255];
  }
  return [r, g, b, 255];
}

const raw = Buffer.alloc(H * (1 + W * 4));
let off = 0;
for (let y = 0; y < H; y++) {
  raw[off++] = 0;
  for (let x = 0; x < W; x++) {
    const [r, g, b, a] = pixel(x, y);
    raw[off++] = r;
    raw[off++] = g;
    raw[off++] = b;
    raw[off++] = a;
  }
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const typeBuf = Buffer.from(type, "ascii");
  const crcInput = Buffer.concat([typeBuf, data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(crcInput) >>> 0);
  return Buffer.concat([len, typeBuf, data, crc]);
}

const CRC_TABLE = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();
function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(W, 0);
ihdr.writeUInt32BE(H, 4);
ihdr[8] = 8;
ihdr[9] = 6;
const png = Buffer.concat([
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  chunk("IHDR", ihdr),
  chunk("IDAT", deflateSync(raw, { level: 9 })),
  chunk("IEND", Buffer.alloc(0)),
]);

const out = process.argv[2] ?? "public/brand/nexterm-icon-1024.png";
writeFileSync(out, png);
console.log(`written ${out} (${png.length} bytes)`);
