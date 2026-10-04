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
      if (json && json !== "{}") return json;
    } catch {
    }
    return Object.prototype.toString.call(e);
  }

  return String(e);
}
