import type { ChatItem, ToolItem } from "./conversation";
import { t } from "../../i18n";

// A run of consecutive tool calls folds into one summary row. Reasoning between two calls folds with them; any other
// item (answer text, diff, confirmation record, plan, outcome) ends the run.
export const TOOL_GROUP_MIN_CALLS = 2;

export type ToolCategory =
  | "command"
  | "read"
  | "list"
  | "write"
  | "search"
  | "screen"
  | "keys"
  | "wait"
  | "query"
  | "other";

const CATEGORY_BY_TOOL: Record<string, ToolCategory> = {
  exec_commands: "command",
  docker_exec: "command",
  read_file: "read",
  list_dir: "list",
  write_file: "write",
  edit_file: "write",
  find_files: "search",
  grep_files: "search",
  // Deprecated alias; history rows still carry it.
  search_files: "search",
  memory_recall: "search",
  read_screen: "screen",
  send_keys: "keys",
  wait_for: "wait",
  db_query: "query",
  db_describe: "query",
  db_list_tables: "query",
  redis_scan: "query",
  redis_command: "query",
};

const CATEGORY_ORDER: ToolCategory[] = ["command", "read", "list", "write", "search", "screen", "keys", "wait", "query", "other"];

export function toolCategory(name: string): ToolCategory {
  return CATEGORY_BY_TOOL[name] ?? "other";
}

function categoryText(category: ToolCategory, count: number): string {
  switch (category) {
    case "command":
      return t("执行了 {count} 次命令", { count });
    case "read":
      return t("读取了 {count} 个文件", { count });
    case "list":
      return t("查看了 {count} 个目录", { count });
    case "write":
      return t("修改了 {count} 个文件", { count });
    case "search":
      return t("搜索了 {count} 次", { count });
    case "screen":
      return t("读取了 {count} 次屏幕", { count });
    case "keys":
      return t("发送了 {count} 次按键", { count });
    case "wait":
      return t("等待了 {count} 次输出", { count });
    case "query":
      return t("查询了 {count} 次数据库", { count });
    default:
      return t("调用了 {count} 个工具", { count });
  }
}

export interface ToolGroup {
  // key is the id of the first item, so the expanded state follows the group while later calls join it.
  key: string;
  start: number;
  // end is exclusive.
  end: number;
  calls: number;
  failed: number;
  counts: Partial<Record<ToolCategory, number>>;
}

function finished(item: ToolItem): boolean {
  return item.summary !== undefined;
}

// toolNeedsAttention is true for a call the folded summary must not hide: one still running, one that failed or was
// refused, one whose subagent has not completed, and one with an unanswered confirmation.
export function toolNeedsAttention(item: ToolItem, pendingConfirmCallIds: ReadonlySet<string>): boolean {
  if (!finished(item) || item.ok !== true || item.panic) return true;
  if (item.subagent && item.subagent.status !== "completed") return true;
  return item.callId !== "" && pendingConfirmCallIds.has(item.callId);
}

export function pendingConfirmCallIds(items: readonly ChatItem[]): Set<string> {
  const out = new Set<string>();
  for (const item of items) {
    if (item.role === "confirm" && item.resolution === undefined && item.callId) out.add(item.callId);
  }
  return out;
}

// groupToolRuns finds the runs of at least TOOL_GROUP_MIN_CALLS consecutive tool calls. A run starts and ends with a
// tool call, so reasoning that streams after the last call stays outside until another call follows it.
export function groupToolRuns(items: readonly ChatItem[]): ToolGroup[] {
  const groups: ToolGroup[] = [];
  let index = 0;
  while (index < items.length) {
    if (items[index].role !== "tool") {
      index++;
      continue;
    }
    const start = index;
    let lastTool = index;
    let cursor = index + 1;
    while (cursor < items.length && (items[cursor].role === "tool" || items[cursor].role === "reasoning")) {
      if (items[cursor].role === "tool") lastTool = cursor;
      cursor++;
    }
    const end = lastTool + 1;
    const counts: Partial<Record<ToolCategory, number>> = {};
    let calls = 0;
    let failed = 0;
    for (let i = start; i < end; i++) {
      const item = items[i];
      if (item.role !== "tool") continue;
      calls++;
      const category = toolCategory(item.name);
      counts[category] = (counts[category] ?? 0) + 1;
      if (finished(item) && (item.ok !== true || item.panic)) failed++;
    }
    if (calls >= TOOL_GROUP_MIN_CALLS) groups.push({ key: items[start].id, start, end, calls, failed, counts });
    index = end;
  }
  return groups;
}

export function toolGroupSummary(group: ToolGroup): string {
  const text = CATEGORY_ORDER.filter((category) => (group.counts[category] ?? 0) > 0)
    .map((category) => categoryText(category, group.counts[category] ?? 0))
    .join(t("，"));
  // Phrases are written to join into a list, so a language with letter case capitalizes only the first one.
  return text.charAt(0).toLocaleUpperCase() + text.slice(1);
}

export function toolGroupFailedText(group: ToolGroup): string {
  return group.failed > 0 ? t("{count} 个失败", { count: group.failed }) : "";
}
