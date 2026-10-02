export type EncChoice = "auto" | "utf-8" | "gbk";

const BASE64_CHUNK_BYTES = 32_766;

export function bytesFromBase64(encoded: string): Uint8Array {
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

export function bytesToBase64(bytes: Uint8Array): string {
  const chunks: string[] = [];
  for (let i = 0; i < bytes.length; i += BASE64_CHUNK_BYTES) {
    const chunk = bytes.subarray(i, i + BASE64_CHUNK_BYTES);
    chunks.push(btoa(String.fromCharCode(...chunk)));
  }
  return chunks.join("");
}

function decodeFatal(bytes: Uint8Array, encoding: "utf-8" | "gbk"): string {
  try {
    return new TextDecoder(encoding, { fatal: true }).decode(bytes);
  } catch {
    throw new Error(`文件包含无效的 ${encoding.toUpperCase()} 数据，已停止打开以避免保存时损坏内容`);
  }
}

export function decodeContent(
  bytes: Uint8Array,
  choice: EncChoice,
): { text: string; encoding: "utf-8" | "gbk" } {
  if (choice !== "auto") {
    return { text: decodeFatal(bytes, choice), encoding: choice };
  }
  try {
    return { text: decodeFatal(bytes, "utf-8"), encoding: "utf-8" };
  } catch {
    return { text: decodeFatal(bytes, "gbk"), encoding: "gbk" };
  }
}
