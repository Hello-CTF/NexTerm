import { useDesktopAccount, useDesktopAccountScope } from "../auth/desktopAccount";
import { formatTime } from "../../ui/format";
import { t } from "../../i18n";

// The desktop account's role and details, shown in SyncCard's connection card (the desktop account card). Like the
// security cards, it waits until the account is resolved on the linked server, so a switch never shows the old one.
export function DesktopAccountSummary() {
  const user = useDesktopAccount();
  const scope = useDesktopAccountScope(user);
  if (!user || !scope) return null;
  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px] text-neutral-200">
      <span className={`nx-badge ${user.role === "superadmin" ? "nx-badge-blue" : ""}`}>
        {user.role === "superadmin" ? t("超级管理员") : t("普通用户")}
      </span>
      {user.state === "reset_required" && <span className="nx-badge nx-badge-amber">{t("待重置密码")}</span>}
      {user.display_name && <span>{t("显示名 {display_name}", { display_name: user.display_name })}</span>}
      {user.last_login_at > 0 && <span className="nx-hint">{t("最近登录 {value}", { value: formatTime(user.last_login_at) })}</span>}
    </div>
  );
}
