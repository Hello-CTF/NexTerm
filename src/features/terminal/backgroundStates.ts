import { t } from "../../i18n";
// Labels for the attention state a background session reports. Free of imports so the website demo can mirror them.

export type BackgroundAttentionState = "running" | "needs_me" | "just_finished" | "idle" | "exited";

export const STATE_LABELS: Record<BackgroundAttentionState, string> = {
  running: t("运行中"),
  needs_me: t("需要处理"),
  just_finished: t("刚完成"),
  idle: t("空闲"),
  exited: t("已退出"),
};

export const STATE_HINTS: Record<BackgroundAttentionState, string> = {
  running: t("终端最近一直有输出"),
  needs_me: t("程序正在等待回应：响铃、发出通知或显示了提示"),
  just_finished: t("刚有命令运行结束"),
  idle: t("没有新的输出，也未在等待回应"),
  exited: t("终端里的程序已经退出"),
};
