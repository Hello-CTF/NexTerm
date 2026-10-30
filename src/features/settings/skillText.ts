import type { SkillEntry } from "./skillsApi";

// Display-only Chinese summaries of the built-in skills. The model keeps reading the English description in the
// skill file; a user-edited description is shown as written.
const BUILTIN_SUMMARY: Record<string, string> = {
  "batch-operations": "在多台主机上安全地执行同一变更：先核对目标，先在一台主机试行，验证后再分批扩大。适用于多主机批量命令；只改一台主机时不需要。",
  "incident-triage": "对异常的 Linux 主机做只读初步排查：负载、内存、磁盘、失败的服务和近期错误。适用于原因未知的服务故障；需要修复或改配置时会先停下来询问你。",
  "screen-confirm": "向交互式终端输入前先读屏幕，输入后再确认状态。适用于编辑器、安装程序、命令行解释器和需要确认的提示；普通的单条命令不需要。",
};

export function skillSummary(entry: SkillEntry): string {
  if (entry.source !== "embedded" || entry.override?.description !== undefined) return entry.description;
  return BUILTIN_SUMMARY[entry.name] ?? entry.description;
}
