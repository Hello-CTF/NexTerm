import { t, translateMessage } from "../../../i18n";
// Wording of the AI approval card and tool results. It imports only i18n so the website demo can mirror the same
// labels; keep it free of app state.

export type ConfirmDecision = "allow" | "allow_session" | "allow_persistent" | "deny";

export const CONFIRM_ACTION_LABELS: Record<ConfirmDecision, string> = {
  allow: t("允许一次"),
  allow_session: t("本会话允许"),
  allow_persistent: t("永久允许"),
  deny: t("拒绝"),
};

export const CONFIRM_ALWAYS_BLOCK_LABEL = t("始终拦截");

// The resolution is stored with the interaction and read back by agentToolData, so it stays in the source language;
// resolutionLabel shows it in the interface language.
export const CONFIRM_RESOLUTION: Record<ConfirmDecision, string> = {
  allow: "已允许一次",
  allow_session: "本会话已允许此类",
  allow_persistent: "已永久允许（可随时在授权管理中撤销）",
  deny: "已拒绝",
};

const RESOLUTION_LABELS: Record<string, string> = {
  已允许一次: t("已允许一次"),
  本会话已允许此类: t("本会话已允许此类"),
  "已永久允许（可随时在授权管理中撤销）": t("已永久允许（可随时在授权管理中撤销）"),
  已拒绝: t("已拒绝"),
};

export function resolutionLabel(resolution: string): string {
  return RESOLUTION_LABELS[resolution] ?? translateMessage(resolution);
}

export const CONFIRM_HELP =
  t("允许一次：仅本次；本会话允许：本次对话不再询问；永久允许：存入授权管理，可撤销。高危、无法判断和生产环境的操作每次都询问。");

// A tool call the user refused: the badge, the screen reader status and the result body.
export const TOOL_USER_DENIED = {
  badge: t("已拒绝"),
  status: t("你已拒绝"),
  body: t("已拒绝本次操作，未执行。"),
} as const;

export function confirmTitle(risk: string | undefined): string {
  if (risk === "danger") return t("高危操作，需要你确认");
  if (risk === "unknowable") return t("无法判断影响，需要你确认");
  return t("需要你确认");
}

export function toolLabel(tool: string): string {
  switch (tool) {
    case "exec_commands":
      return t("执行命令");
    case "write_file":
      return t("写入文件");
    case "edit_file":
      return t("修改文件");
    case "send_keys":
      return t("发送按键");
    case "read_screen":
      return t("读取屏幕");
    case "terminal_create":
      return t("新建终端");
    case "docker_exec":
      return t("容器内执行");
    case "docker_control":
      return t("容器操作");
    case "db_query":
      return t("数据库查询");
    case "redis_command":
      return t("Redis 命令");
    case "send_reminder":
      return t("定时提醒");
    case "shell_history":
      return t("读取 shell 历史");
    default:
      return tool;
  }
}
