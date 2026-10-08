export type EncChoice = "auto" | "utf-8" | "gbk";

export { bytesToBase64 } from "../../ui/base64";

export function bytesFromBase64(encoded: string): Uint8Array {
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes;
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
