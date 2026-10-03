// 文件操作（重命名 / 权限 / 校验值）的纯逻辑：输入预检与常量。
//
// 边界约定（M58 调查结论，M60 落地）：
//   · 这里只做**输入形状预检** —— 空名、跨路径、非八进制、超界 mode、未知算法
//     在发 RPC 之前就挡住，给用户一句能看懂的话；
//   · 预检**不是授权**：存在性、能否覆盖、权限是否允许、算法是否被接受，最终都
//     由后端裁决，后端的拒绝原样呈现。前端状态绝不替代后端授权；
//   · 常量与后端实现对齐（Rust 参考实现：checksum 只认 md5/sha256，缺省 sha256；
//     chmod 在 Windows 类目标上由后端报「不支持」）。
import type { FileEntryDto } from "../../ipc/types";

/** 重命名只改「同目录下的名字」，不接受任何路径成分 —— 移动不是 rename 的职责。 */
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

/**
 * 解析八进制权限串（`644` / `0644`）。
 *
 * 只接受 1–4 位八进制且数值 ≤ 0o777：setuid/setgid/sticky 这类高位不在这套
 * 界面里提供 —— 需要它们的用户应该用终端，而不是文件管理器。
 */
export function parseOctalMode(input: string): ParsedMode {
  const text = input.trim();
  if (!text) return { ok: false, reason: "权限不能为空" };
  if (!/^[0-7]{1,4}$/.test(text))
    return { ok: false, reason: "权限只能是 0–7 的八进制数字（最多 4 位）" };
  const mode = parseInt(text, 8);
  if (mode > 0o777) return { ok: false, reason: "权限超出范围（最大 777）" };
  return { ok: true, mode };
}

/** 能解析就返回数值，否则 null（条目 mode 可能是 `drwxr-xr-x` 这种展示串）。 */
export function tryParseOctalMode(input: string): number | null {
  const parsed = parseOctalMode(input);
  return parsed.ok ? parsed.mode : null;
}

/** 数值 → 展示用八进制串（`493` → `"755"`）。 */
export function formatMode(mode: number): string {
  return mode.toString(8).padStart(3, "0");
}

/** chmod 输入框的默认值：条目 mode 是八进制串就用，否则留空让用户填。 */
export function octalDefaultFromEntry(mode: string): string {
  const parsed = tryParseOctalMode(mode);
  return parsed === null ? "" : formatMode(parsed);
}

/** 新权限是否移除了所有者已有的读或写（可能是自我锁死，需要危险确认）。 */
export function losesOwnerAccess(current: number, next: number): boolean {
  return ((current & 0o600) & ~next) !== 0;
}

/** 同目录下是否已有同名项（覆盖预判；能不能覆盖最终由后端决定）。 */
export function findSibling(
  siblings: FileEntryDto[],
  name: string,
): FileEntryDto | undefined {
  return siblings.find((e) => e.name === name);
}

/** 后端支持的校验算法（Rust 参考实现：md5 / sha256，其余报「不支持的校验算法」）。 */
export const CHECKSUM_ALGOS: { key: string; label: string; hint: string }[] = [
  { key: "sha256", label: "SHA-256", hint: "默认；更长、更安全" },
  { key: "md5", label: "MD5", hint: "仅用于与旧系统对账" },
];

export const DEFAULT_CHECKSUM_ALGO = "sha256";
