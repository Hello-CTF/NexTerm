/**
 * 把内核（Rust）抛上来的错误渲染成可读文本。
 *
 * 为什么要单独一个模块：`AppError` 是**结构化对象**（`{ code, message }`），
 * 直接 `String(e)` 只会得到 `"[object Object]"`，真实原因当场丢失——
 * 之前终端里那句 `[attach 失败] [object Object]` 就是这么来的，
 * 排查时等于什么都没说。
 *
 * 顺带修一个更隐蔽的坑：Tauri 的 invoke 被 reject 时给的是**普通对象**而不是
 * `Error` 实例，所以 `e instanceof Error` 这条路走不通，必须单独认一遍结构。
 */
export function describeError(e: unknown): string {
  if (e == null) return "未知错误";
  if (typeof e === "string") return e;
  if (typeof e === "number" || typeof e === "boolean") return String(e);
  if (e instanceof Error) return e.message || e.name;

  if (typeof e === "object") {
    const o = e as { code?: unknown; message?: unknown };
    const code = typeof o.code === "string" ? o.code : "";
    const message = typeof o.message === "string" ? o.message : "";
    if (code && message) return `${code}: ${message}`;
    if (message) return message;
    if (code) return code;
    try {
      const json = JSON.stringify(e);
      // JSON.stringify 对纯对象也可能给回 "{}"，那还不如看构造名
      if (json && json !== "{}") return json;
    } catch {
      // 循环引用之类，落到下面的兜底
    }
    return Object.prototype.toString.call(e);
  }

  return String(e);
}
