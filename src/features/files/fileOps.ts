import { t } from "../../i18n";
import type { PathStyle } from "./pathUtils";

const WINDOWS_RESERVED_NAME = /^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$/i;

// validateEntryName checks a single path component for the target: a POSIX name may hold a backslash, while a
// Windows name also rejects reserved device names, a trailing dot or space and the characters <>:"|?*.
export function validateEntryName(name: string, pathStyle: PathStyle = "unknown"): string | null {
  if (!name.trim()) return t("名称不能为空");
  if (name !== name.trim()) return t("名称首尾不能是空白字符");
  if (name.includes("/") || (pathStyle !== "posix" && name.includes("\\")))
    return t("名称不能包含路径分隔符（重命名不能用来移动文件/目录）");
  if (name === "." || name === "..") return t("名称不能是 . 或 ..");
  if (name.includes("\0")) return t("名称不能包含 NUL 字符");
  if (pathStyle === "windows") {
    if (/[<>:"|?*\x01-\x1f]/.test(name)) return t('Windows 名称不能包含 < > : " | ? * 或控制字符');
    if (/[. ]$/.test(name)) return t("Windows 名称不能以点或空格结尾");
    if (WINDOWS_RESERVED_NAME.test(name)) return t("「{name}」是 Windows 保留的设备名，不能用作文件名", { name });
  }
  return null;
}

export type ParsedMode = { ok: true; mode: number } | { ok: false; reason: string };

export function parseOctalMode(input: string): ParsedMode {
  const text = input.trim();
  if (!text) return { ok: false, reason: t("权限不能为空") };
  if (!/^[0-7]{1,4}$/.test(text))
    return { ok: false, reason: t("权限只能是 0-7 的八进制数字（最多 4 位）") };
  const mode = parseInt(text, 8);
  if (mode > 0o777) return { ok: false, reason: t("权限超出范围（最大为八进制 777）") };
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

export const CHECKSUM_ALGOS: { key: string; label: string; hint: string }[] = [
  { key: "sha256", label: "SHA-256", hint: t("默认；更长、更安全") },
  { key: "md5", label: "MD5", hint: t("仅用于匹配旧系统的校验值") },
];

export const DEFAULT_CHECKSUM_ALGO = "sha256";

export interface FileCapabilities {
  chmod: string;
  pack: boolean;
  extract: boolean;
  caseInsensitive: boolean;
}

// The backend's declared capabilities decide; the session kind is the fallback for a backend that reports none.
export function packDownloadSupported(sessionKind: string, capabilities?: FileCapabilities | null): boolean {
  if (capabilities) return capabilities.pack;
  return sessionKind === "ssh" || sessionKind === "device" || sessionKind === "local";
}

export function extractSupported(capabilities?: FileCapabilities | null): boolean {
  return capabilities ? capabilities.extract : true;
}

export type ChmodSupport = "posix" | "readonly" | "none";

export function chmodSupport(capabilities?: FileCapabilities | null): ChmodSupport {
  const chmod = capabilities?.chmod;
  return chmod === "readonly" || chmod === "none" ? chmod : "posix";
}

// Windows maps only the owner write bit (0o200) to the read-only attribute; the other bits are ignored there.
export const WINDOWS_WRITABLE_MODE = 0o666;
export const WINDOWS_READONLY_MODE = 0o444;

export function windowsReadOnly(mode: string): boolean | null {
  const parsed = tryParseOctalMode(mode);
  if (parsed !== null) return (parsed & 0o200) === 0;
  // Go's FileMode.String: type letters, then nine rwx permission characters.
  const perms = mode.slice(-9);
  return /^[r-][w-][xsStT-][r-][w-][xsStT-][r-][w-][xsStT-]$/.test(perms) ? perms[1] === "-" : null;
}
