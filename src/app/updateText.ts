import type { UpdateInstallPhase } from "./useUpdate";
import { t } from "../i18n";

// A restart quits and relaunches the app: temporary terminals live in the app process and end, while durable
// terminals run in the detached terminal component and keep running.
export const RESTART_NOTE = t("重启会关闭并重新打开 NexTerm：临时终端会结束，持久终端继续运行。");

// restartErrorText words a failed restart by what it left behind: nothing installed, or an installed update that
// only needs the app reopened.
export function restartErrorText(failure: "apply" | "relaunch" | null, error: string): string {
  if (failure === "apply") return t("更新未安装：{error}", { error });
  return t("重启失败：{restartError}。请手动退出并重新打开应用。", { restartError: error });
}

export function installPhaseLabel(phase: UpdateInstallPhase | null): string {
  if (phase === "verify") return t("正在校验");
  if (phase === "install") return t("正在安装");
  return t("正在下载");
}
