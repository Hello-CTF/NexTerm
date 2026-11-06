// Wording of the AI approval card and tool results. This module has no imports so the website demo can mirror the
// same labels; keep it free of app state.

export type ConfirmDecision = "allow" | "allow_session" | "allow_persistent" | "deny";

export const CONFIRM_ACTION_LABELS: Record<ConfirmDecision, string> = {
  allow: "允许一次",
  allow_session: "本会话允许",
  allow_persistent: "永久允许",
  deny: "拒绝",
};

export const CONFIRM_ALWAYS_BLOCK_LABEL = "始终拦截";

export const CONFIRM_RESOLUTION: Record<ConfirmDecision, string> = {
  allow: "已允许一次",
  allow_session: "本会话已允许此类",
  allow_persistent: "已永久允许（可随时在授权管理中撤销）",
  deny: "已拒绝",
};

export const CONFIRM_HELP =
  "允许一次：仅本次；本会话允许：本次对话不再询问；永久允许：存入授权管理，可撤销。高危、无法判断和生产环境的操作每次都询问。";

// A tool call the user refused: the badge, the screen reader status and the result body.
export const TOOL_USER_DENIED = {
  badge: "已拒绝",
  status: "你已拒绝",
  body: "已拒绝本次操作，未执行。",
} as const;

export function confirmTitle(risk: string | undefined): string {
  if (risk === "danger") return "高危操作，需要你确认";
  if (risk === "unknowable") return "无法判断影响，需要你确认";
  return "需要你确认";
}

export function toolLabel(tool: string): string {
  switch (tool) {
    case "exec_commands":
      return "执行命令";
    case "write_file":
      return "写入文件";
    case "edit_file":
      return "修改文件";
    case "send_keys":
      return "发送按键";
    case "read_screen":
      return "读取屏幕";
    case "terminal_create":
      return "新建终端";
    case "docker_exec":
      return "容器内执行";
    case "docker_control":
      return "容器操作";
    case "db_query":
      return "数据库查询";
    case "redis_command":
      return "Redis 命令";
    case "send_reminder":
      return "定时提醒";
    case "shell_history":
      return "读取 shell 历史";
    default:
      return tool;
  }
}
