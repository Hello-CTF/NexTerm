import type { FileEntryDto } from "../../ipc/types";

export function validateEntryName(name: string): string | null {
  if (!name.trim()) return "名称不能为空";
  if (name !== name.trim()) return "名称首尾不能是空白字符";
  if (name.includes("/") || name.includes("\\"))
    return "名称不能包含路径分隔符（重命名不能用来移动文件/目录）";
  if (name === "." || name === "..") return "名称不能是 . 或 ..";
  if (name.includes("\0")) return "名称不能包含 NUL 字符";
  return null;
}

export type ParsedMode = { ok: true; mode: number } | { ok: false; reason: string };

export function parseOctalMode(input: string): ParsedMode {
  const text = input.trim();
  if (!text) return { ok: false, reason: "权限不能为空" };
  if (!/^[0-7]{1,4}$/.test(text))
    return { ok: false, reason: "权限只能是 0-7 的八进制数字（最多 4 位）" };
  const mode = parseInt(text, 8);
  if (mode > 0o777) return { ok: false, reason: "权限超出范围（最大 777）" };
  return { ok: true, mode };
}

export function tryParseOctalMode(input: string): number | null {
  const parsed = parseOctalMode(input);
  return parsed.ok ? parsed.mode : null;
}

export function formatMode(mode: number): string {
  return mode.toString(8).padStart(3, "0");
}

export function octalDefaultFromEntry(mode: string): string {
  const parsed = tryParseOctalMode(mode);
  return parsed === null ? "" : formatMode(parsed);
}

export function losesOwnerAccess(current: number, next: number): boolean {
  return ((current & 0o600) & ~next) !== 0;
}

export function findSibling(
  siblings: FileEntryDto[],
  name: string,
): FileEntryDto | undefined {
  return siblings.find((e) => e.name === name);
}

export const CHECKSUM_ALGOS: { key: string; label: string; hint: string }[] = [
  { key: "sha256", label: "SHA-256", hint: "默认；更长、更安全" },
  { key: "md5", label: "MD5", hint: "仅用于与旧系统对账" },
];

export const DEFAULT_CHECKSUM_ALGO = "sha256";
