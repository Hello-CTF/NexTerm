import { t } from "../../i18n";
// The label a container's state gets in the Docker panel. Free of imports so the website demo can mirror it.
export function containerStateLabel(state: string): string {
  if (state === "running") return t("运行中");
  if (state === "exited") return t("已停止");
  return state;
}
