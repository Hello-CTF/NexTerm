const NXBM_HEADER_SIZE = 64;
const NXBM_FLAG_ENCRYPTED = 1;

export function buildPlaintextNxbm(payload: string): Uint8Array<ArrayBuffer> {
  const body = new TextEncoder().encode(payload);
  const header = new Uint8Array(NXBM_HEADER_SIZE);
  header.set([0x4e, 0x58, 0x42, 0x4d, 1, 0], 0);
  header[18] = 16;
  header[35] = 12;
  new DataView(header.buffer).setBigUint64(48, BigInt(body.length), false);
  const out = new Uint8Array(NXBM_HEADER_SIZE + body.length);
  out.set(header, 0);
  out.set(body, NXBM_HEADER_SIZE);
  return out;
}

export type ParsedNxbm =
  | { kind: "plaintext"; payload: string }
  | { kind: "encrypted" }
  | { kind: "invalid"; error: string };

export function parseNxbmContainer(buffer: ArrayBuffer): ParsedNxbm {
  const bytes = new Uint8Array(buffer);
  if (
    bytes.length < NXBM_HEADER_SIZE ||
    bytes[0] !== 0x4e ||
    bytes[1] !== 0x58 ||
    bytes[2] !== 0x42 ||
    bytes[3] !== 0x4d
  ) {
    return { kind: "invalid", error: "只支持 .nxbm 资产包文件" };
  }
  if (bytes[4] !== 1) {
    return { kind: "invalid", error: `不支持的资产包版本（${bytes[4]}）` };
  }
  if ((bytes[5] & NXBM_FLAG_ENCRYPTED) !== 0) {
    return { kind: "encrypted" };
  }
  const plainLen = Number(new DataView(buffer).getBigUint64(48, false));
  const body = bytes.subarray(NXBM_HEADER_SIZE);
  if (body.length !== plainLen) {
    return { kind: "invalid", error: "资产包长度字段不一致" };
  }
  return { kind: "plaintext", payload: new TextDecoder().decode(body) };
}
