import { describeError } from "./errorText";

// Maps a raw failure to a short sentence an ordinary user can act on. The raw text stays available for a details disclosure.
export function friendlyErrorMessage(e: unknown): string {
  const raw = describeError(e);
  if (/非 JSON|HTTP\s*(404|5\d\d)|Failed to fetch|NetworkError/i.test(raw)) {
    return "暂时无法连接服务，服务可能正在重启或版本不一致。请稍后重试，仍然失败请刷新页面。";
  }
  if (/凭据库已锁定|vault_locked/i.test(raw)) return "凭据库已锁定。请先解锁，再重试。";
  if (/超时|timeout|timed out/i.test(raw)) return "等待超时。请检查网络后重试。";
  if (/没有权限|forbidden|HTTP\s*40[13]/i.test(raw)) return "没有权限执行此操作。请重新登录或联系管理员。";
  return "请重试，仍然失败请查看详情。";
}

export function friendlyErrorDetail(e: unknown): string {
  return describeError(e);
}
