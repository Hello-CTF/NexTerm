// Docker 洞察视图的**展示级**脱敏。
//
// 边界先说清楚：这里做的只是「不在界面上明文渲染敏感值」。真正的所有权校验与
// 强制遮蔽必须在内核 RPC 边界完成（session → transport → container 归属、审计），
// 前端既不构成授权，也不能替代内核遮蔽 —— 前端脱敏被绕过 ≠ 拿到了越权数据，
// 前端不脱敏 ≠ 有权限看。UI 上所有用到本模块的地方都要带着这个口径提示。

/** 命中即遮蔽的键名（小写匹配）。env 名、label 键、inspect 里的任意对象键共用。 */
const SENSITIVE_KEY = /(pass|secret|token|key|credential|auth|pwd|private)/i;

/** 命中即打「敏感」标的容器内文件名（只列目录，本就没有读取内容的 RPC）。 */
const SENSITIVE_FILE =
 /(^\.env)|(\.(pem|key|p12|pfx|jks|keystore|kubeconfig)$)|(^(id_rsa|id_dsa|id_ecdsa|id_ed25519)(\.|$))|(credential|secret|token|password|passwd)/i;

/** 遮蔽占位符。用固定文案而不是星号长度，避免按长度猜原值。 */
export const REDACTED_MARK = "•••（已遮蔽）";

export function isSensitiveName(name: string): boolean {
  return SENSITIVE_KEY.test(name);
}

export function isSensitiveFileName(name: string): boolean {
  return SENSITIVE_FILE.test(name.trim());
}

/** `K=V` 形式 env 项拆键值；没有 `=` 的按无值处理（键本身可能敏感）。 */
export function splitEnvEntry(entry: string): { name: string; value: string } {
  const i = entry.indexOf("=");
  if (i < 0) return { name: entry, value: "" };
  return { name: entry.slice(0, i), value: entry.slice(i + 1) };
}

export function isSensitiveEnvEntry(entry: string): boolean {
  return isSensitiveName(splitEnvEntry(entry).name);
}

/**
 * 递归脱敏 inspect JSON（`docker inspect` 的原始结构：对象或单元素数组）。
 *
 * 规则：
 *  - 对象键命中 SENSITIVE_KEY → 整棵子树替换为 REDACTED_MARK；
 *  - `Env` 数组（`Config.Env` 的 `K=V` 字符串）→ 键敏感时值遮蔽；
 *  - 其余原样保留 —— 用户排查问题需要看到真实结构，只遮值不遮形。
 *
 * `reveal` 为 true 时原样返回（「显示敏感值」开关打开后的路径）。
 */
export function redactInspectTree(value: unknown, reveal: boolean): unknown {
  if (reveal) return value;
  return walk(value, undefined);
}

function walk(value: unknown, keyHint: string | undefined): unknown {
  if (keyHint && isSensitiveName(keyHint)) return REDACTED_MARK;

  if (Array.isArray(value)) {
    // Env 是 string[]（K=V），逐条按键名遮蔽；其余数组递归。
    if (keyHint === "Env") {
      return value.map((entry) => {
        if (typeof entry !== "string") return walk(entry, undefined);
        const { name } = splitEnvEntry(entry);
        return isSensitiveName(name) ? `${name}=${REDACTED_MARK}` : entry;
      });
    }
    // 数组元素带一个 `键[]` 提示，供对象分支做容器内判断（见下）。
    return value.map((item) => walk(item, keyHint ? `${keyHint}[]` : undefined));
  }

  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      // Mounts[].Name 是卷名：`Name` 键名本身不敏感，键值规则盖不到，
      // 但卷名可能带敏感词（如 db-passwords）—— 按值补一刀。
      if (keyHint === "Mounts[]" && k === "Name" && typeof v === "string" && isSensitiveName(v)) {
        out[k] = REDACTED_MARK;
        continue;
      }
      out[k] = walk(v, k);
    }
    return out;
  }

  return value;
}

/** 从 inspect JSON 里防御性取字符串字段（路径用 `.` 分隔）。 */
export function pickInspectString(root: unknown, path: string): string {
  let node: unknown = root;
  for (const part of path.split(".")) {
    if (!node || typeof node !== "object") return "";
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : "";
}

/** 同上，取数字并渲染成字符串；取不到返回 ""。 */
export function pickInspectNumber(root: unknown, path: string): string {
  let node: unknown = root;
  for (const part of path.split(".")) {
    if (!node || typeof node !== "object") return "";
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "number" ? String(node) : "";
}
