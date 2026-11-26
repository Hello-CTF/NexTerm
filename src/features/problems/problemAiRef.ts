import { sanitizeWorkspaceProblemText, useUi, type WorkspaceProblem } from "../../app/store";
import { t } from "../../i18n";
import { clipRefText, newRefId, refPreview, REF_TEXT_RUNES, singleLine } from "../ai/refText";

export function workspaceProblemCopyText(problem: WorkspaceProblem): string {
  const lines = [
    t("时间：{toLocaleString}", { toLocaleString: new Date(problem.createdAt).toLocaleString() }),
    t("操作：{operation}", { operation: problem.operation }),
    t("目标：{title} · {identity}", { title: problem.target.title, identity: problem.target.identity }),
  ];
  if (problem.target.schema !== undefined) lines.push(`schema：${problem.target.schema || t("默认")}`);
  lines.push(t("摘要：{summary}", { summary: problem.summary }), t("详情：{detail}", { detail: problem.detail }));
  return sanitizeWorkspaceProblemText(lines.join("\n"));
}

const SOURCE_LABEL: Record<WorkspaceProblem["source"], string> = {
  "sql-query": t("SQL 执行"),
  "sql-schemas": t("读取 schema 列表"),
  "sql-tables": t("读取表列表"),
  connect: t("连接"),
};

// problemRefText is what the AI is shown of a problem: the copied details, where it was recorded, the statement
// involved and whether its outcome is known.
export function problemRefText(problem: WorkspaceProblem): string {
  const lines = [workspaceProblemCopyText(problem), t("来源：{source}", { source: SOURCE_LABEL[problem.source] ?? problem.source })];
  if (problem.outcome === "unknown") lines.push(t("结果未知，请先确认数据库状态"));
  if (problem.detailTruncated) lines.push(t("详情已截断"));
  if (problem.sql !== undefined) lines.push(problem.sqlTruncated ? t("SQL（副本不完整）：") : "SQL：", problem.sql);
  return sanitizeWorkspaceProblemText(lines.join("\n"));
}

// askAiAboutProblem queues a recorded problem as a problem reference for the AI panel and opens the panel. The
// problem travels as a data reference, never as part of the user's message.
export function askAiAboutProblem(problem: WorkspaceProblem): boolean {
  const { text, clipped } = clipRefText(problemRefText(problem), "head");
  const ui = useUi.getState();
  if (clipped) ui.pushToast("info", t("问题详情较长，AI 只会看到前 {limit} 个字符", { limit: REF_TEXT_RUNES }));
  const id = newRefId("problem");
  ui.queueAiRef({
    kind: "problem",
    id,
    label: t("问题 · {preview}", { preview: refPreview(problem.summary) }),
    detail: singleLine(`${problem.operation} · ${problem.target.title} · ${problem.target.identity}`),
    refId: id,
    text,
  });
  ui.setRightOpen(true);
  return true;
}
