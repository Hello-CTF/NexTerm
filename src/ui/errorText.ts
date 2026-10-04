const ERROR_CODE_LABELS: Record<string, string> = {
  internal: "内部错误",
  bad_param: "参数错误",
  io: "IO 错误",
  db: "数据库错误",
  crypto: "加密错误",
  decrypt: "凭据解密失败",
  bad_master_password: "主密码错误",
  vault_locked: "凭据库已锁定",
  vault_not_init: "凭据库尚未初始化",
  vault_already_init: "凭据库已初始化",
  host_key_pending: "主机指纹待确认",
  not_found: "资源不存在",
  unsupported: "不支持",
  disconnected: "已断开",
  timeout: "超时",
  forbidden: "没有权限",
  not_controller: "没有控制权",
  needs_confirm: "需要确认",
};

export function describeError(e: unknown): string {
  if (e == null) return "未知错误";
  if (typeof e === "string") return e;
  if (typeof e === "number" || typeof e === "boolean") return String(e);
  if (e instanceof Error) return e.message || e.name;

  if (typeof e === "object") {
    const o = e as { code?: unknown; message?: unknown };
    const code = typeof o.code === "string" ? o.code : "";
    const message = typeof o.message === "string" ? o.message : "";
    if (code && message) {
      const label = ERROR_CODE_LABELS[code];
      if (!label) return `${code}: ${message}`;
      if (message.startsWith(label)) return message;
      return `${label}：${message}`;
    }
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
