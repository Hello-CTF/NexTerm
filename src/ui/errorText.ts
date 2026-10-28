// Plain-language text per error code: what failed and what to do next. The raw technical text stays available through
// describeErrorDetail for a 「详情」 disclosure.
const ERROR_CODE_TEXT: Record<string, string> = {
  internal: "应用内部出现错误，请稍后重试；仍然失败请查看详情",
  bad_param: "输入内容不正确，请检查后重试",
  io: "读写文件失败，请检查磁盘空间和文件权限后重试",
  db: "读写本地数据失败，请重试；仍然失败请重启应用",
  db_migrate: "升级本地数据失败，请重启应用后重试",
  crypto: "加密处理失败，请重试",
  decrypt: "无法解密已保存的凭据，请确认保护密码，或重新输入凭据",
  bad_master_password: "保护密码不正确，请重新输入",
  vault_locked: "凭据库已锁定，请先解锁",
  vault_not_init: "凭据库尚未初始化，请先在「设置 → 凭据保护」中开启",
  vault_already_init: "凭据库已初始化，无需重复操作",
  host_key_pending: "请先确认主机指纹，核对无误后再选择信任",
  not_found: "未找到对应内容，可能已被删除",
  unsupported: "当前环境不支持该操作",
  disconnected: "连接已断开，请重新连接后再试",
  timeout: "等待超时，请检查网络后重试",
  forbidden: "无权执行该操作",
  conflict: "内容已被其他操作修改，请刷新后重试",
  metadata_conflict: "名称已被其他操作修改，请刷新后重试",
  mfa_enrollment_required: "需要先绑定二次验证，请在账号设置中完成绑定后再继续",
  cancelled: "操作已取消",
  needs_confirm: "该操作需要确认后才能继续",
  ssh: "SSH 连接或操作失败，请检查主机地址、账号和网络后重试",
  sftp: "文件传输失败，请检查连接和路径权限后重试",
  winrm: "Windows 远程连接失败，请检查主机地址、账号和 WinRM 设置后重试",
  docker_not_installed: "目标主机未安装 Docker",
  docker_unavailable: "无法访问 Docker，请确认 Docker 服务已运行",
  docker_forbidden: "当前账号无 Docker 权限，请使用有权限的账号或将其加入 docker 用户组",
};

const GENERIC_FAILURE = "操作未成功，请稍后重试；仍然失败请查看详情";
const BAD_RESPONSE = "服务未返回有效结果，请稍后重试；若持续出现，请检查服务版本或网络代理";
const DIAL_FAILURE = "无法连接到目标主机，请检查地址、端口和网络后重试";
const NETWORK_FAILURE = "网络请求失败，请检查网络连接后重试";

const CJK = /[\u4e00-\u9fff]/;
// A leading legacy "xx错误: " label is not itself an explanation.
const LEGACY_LABEL = /^[^:：]{1,12}(?:错误|失败)[:：]\s*/;
const RAW_HTTP = /HTTP\s*\d{3}|非\s*JSON|<!doctype|<html/i;
const DIAL_ERROR = /connection refused|ECONNREFUSED|no such host|i\/o timeout|dial tcp|network is unreachable|ETIMEDOUT/i;
const NETWORK_ERROR = /^(?:TypeError:\s*)?(?:Failed to fetch|NetworkError|Load failed)/i;

// readable reports whether a message is already a sentence a user can act on: Chinese text beyond a bare label and
// free of raw HTTP leftovers.
function stripHttp(message: string): string {
  return message.replace(/\s*[（(]\s*HTTP\s*\d{3}\s*[)）]/g, "");
}

function readable(message: string): boolean {
  const rest = stripHttp(message).replace(LEGACY_LABEL, "");
  if (RAW_HTTP.test(rest)) return false;
  return CJK.test(rest);
}

function plain(code: string, message: string): string {
  if (message && readable(message)) return stripHttp(message);
  if (RAW_HTTP.test(message)) return BAD_RESPONSE;
  if (NETWORK_ERROR.test(message)) return NETWORK_FAILURE;
  if (DIAL_ERROR.test(message)) return DIAL_FAILURE;
  const text = ERROR_CODE_TEXT[code] ?? GENERIC_FAILURE;
  // A rejected input or an unsupported request carries the reason the user needs to correct it; keep it visible.
  if (REASON_CODES.has(code) && shortReason(message)) return `${text}（原因：${message.trim()}）`;
  return text;
}

const REASON_CODES = new Set(["bad_param", "unsupported"]);

function shortReason(message: string): boolean {
  const trimmed = message.trim();
  return trimmed !== "" && trimmed.length <= 160 && !RAW_HTTP.test(trimmed) && !/\n/.test(trimmed);
}

// Most callers show describeError(e) in a toast without a detail argument. The raw text of recent errors is remembered
// by the plain text it produced so the toast can still offer it under 「详情」.
const recentDetails = new Map<string, string>();

function remember(text: string, e: unknown): string {
  const detail = describeErrorDetail(e);
  if (detail && text.length >= 6) {
    recentDetails.delete(text);
    recentDetails.set(text, detail);
    if (recentDetails.size > 24) recentDetails.delete(recentDetails.keys().next().value as string);
  }
  return text;
}

/** detailForText finds the raw text behind a message built from describeError (the message may carry a prefix). */
export function detailForText(text: string): string | null {
  for (const [key, detail] of [...recentDetails.entries()].reverse()) {
    if (text.includes(key)) return detail;
  }
  return null;
}

export function describeError(e: unknown): string {
  return remember(describeErrorText(e), e);
}

function describeErrorText(e: unknown): string {
  if (e == null) return "未知错误";
  if (typeof e === "string") return e;
  if (typeof e === "number" || typeof e === "boolean") return String(e);
  if (e instanceof Error) {
    if (NETWORK_ERROR.test(e.message)) return NETWORK_FAILURE;
    if (readable(e.message)) return stripHttp(e.message);
    if (RAW_HTTP.test(e.message)) return BAD_RESPONSE;
    if (DIAL_ERROR.test(e.message)) return DIAL_FAILURE;
    return e.message || e.name;
  }

  if (typeof e === "object") {
    const o = e as { code?: unknown; message?: unknown };
    const code = typeof o.code === "string" ? o.code : "";
    const message = typeof o.message === "string" ? o.message : "";
    if (code || message) return plain(code, message);
    try {
      const json = JSON.stringify(e);
      if (json && json !== "{}") return GENERIC_FAILURE;
    } catch {
      // fall through
    }
    return GENERIC_FAILURE;
  }

  return String(e);
}

// describeErrorDetail returns the raw technical text for a 「详情」 disclosure, or null when it adds nothing beyond
// what describeError already shows.
export function describeErrorDetail(e: unknown): string | null {
  let raw = "";
  if (e instanceof Error) {
    raw = e.message;
  } else if (typeof e === "object" && e !== null) {
    const o = e as { code?: unknown; message?: unknown; detail?: unknown };
    const parts: string[] = [];
    if (typeof o.code === "string" && o.code) parts.push(`code=${o.code}`);
    if (typeof o.message === "string" && o.message) parts.push(o.message);
    if (typeof o.detail === "string") parts.push(o.detail);
    else if (o.detail !== undefined && o.detail !== null) {
      try {
        parts.push(JSON.stringify(o.detail));
      } catch {
        // ignore unserialisable detail
      }
    }
    raw = parts.join(" | ");
  } else if (typeof e === "string") {
    raw = e;
  }
  raw = raw.trim();
  if (!raw || raw === describeErrorText(e)) return null;
  return raw;
}
