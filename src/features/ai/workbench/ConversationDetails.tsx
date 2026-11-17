import { useEffect, useMemo, useState, type ReactNode } from "react";
import { aiApi, modelApi } from "../../../ipc/commands";
import type { AiConversationApprovalDto, AiConversationSummaryDto, AiRunDto, ModelProfile } from "../../../ipc/types";
import { describeError } from "../../../ui/errorText";
import type { ChatItem } from "../conversation";
import { toolLabel } from "../panel/aiSidebarShared";
import { approvalDecisionLabel, conversationStatusLabel } from "./workbenchShared";
import { formatDateTime, formatNumber, t } from "../../../i18n";

// Runs read for the details column; older runs are counted in the conversation summary only.
export const DETAIL_RUN_LIMIT = 50;

function runStatusLabel(status: string): string {
  switch (status) {
    case "running":
      return t("运行中");
    case "interrupted":
      return t("已中断");
    case "completed":
      return t("已完成");
    case "canceled":
      return t("已停止");
    case "failed":
      return t("失败");
    case "expired":
      return t("已过期");
    default:
      return status;
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return t("{value} 毫秒", { value: Math.max(0, Math.round(ms)) });
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return t("{value} 秒", { value: seconds });
  return t("{minutes} 分 {seconds} 秒", { minutes: Math.floor(seconds / 60), seconds: seconds % 60 });
}

function when(ms: number): string {
  return formatDateTime(ms, { dateStyle: "short", timeStyle: "short" });
}

// changedFiles lists each changed path once with the number of saved changes to it, in first-change order.
export function changedFiles(items: readonly ChatItem[]): { path: string; changes: number }[] {
  const counts = new Map<string, number>();
  for (const item of items) {
    if (item.role !== "diff") continue;
    counts.set(item.path, (counts.get(item.path) ?? 0) + 1);
  }
  return [...counts].map(([path, changes]) => ({ path, changes }));
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-1.5">
      <h3 className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">{title}</h3>
      {children}
    </section>
  );
}

export function ConversationDetails({
  summary,
  hostLabel,
  items,
}: {
  summary: AiConversationSummaryDto;
  hostLabel: string;
  items: readonly ChatItem[];
}) {
  const [runs, setRuns] = useState<AiRunDto[] | null>(null);
  const [approvals, setApprovals] = useState<AiConversationApprovalDto[] | null>(null);
  const [profiles, setProfiles] = useState<ModelProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const { id, updatedAt, status } = summary;

  useEffect(() => {
    let current = true;
    setError(null);
    Promise.all([aiApi.runs(id, DETAIL_RUN_LIMIT), aiApi.conversationApprovals(id)]).then(
      ([runRows, approvalRows]) => {
        if (!current) return;
        setRuns(Array.isArray(runRows) ? runRows : []);
        setApprovals(approvalRows);
      },
      (e: unknown) => {
        if (current) setError(describeError(e));
      },
    );
    return () => {
      current = false;
    };
    // A conversation's runs change with its activity, so the details follow its last update and status.
  }, [id, updatedAt, status]);

  useEffect(() => {
    let current = true;
    Promise.resolve(modelApi.overview()).then(
      (view) => {
        if (current) setProfiles(view?.profiles ?? []);
      },
      () => {},
    );
    return () => {
      current = false;
    };
  }, []);

  const files = useMemo(() => changedFiles(items), [items]);
  const modelOf = (profileId?: string) => {
    if (!profileId) return t("默认模型");
    const profile = profiles.find((candidate) => candidate.id === profileId);
    return profile ? `${profile.name} · ${profile.model}` : t("已删除的模型配置");
  };
  const latestModel = runs && runs.length > 0 ? modelOf(runs[0].profileId) : "-";

  return (
    <div className="flex flex-col gap-4 p-3 text-[12px]" data-testid="ai-workbench-details">
      <Section title={t("概要")}>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
          <dt className="text-neutral-500">{t("主机")}</dt>
          <dd className="truncate" title={hostLabel}>{hostLabel}</dd>
          <dt className="text-neutral-500">{t("模型")}</dt>
          <dd className="truncate" title={latestModel}>{latestModel}</dd>
          <dt className="text-neutral-500">{t("状态")}</dt>
          <dd>{conversationStatusLabel(summary.status)}</dd>
          <dt className="text-neutral-500">{t("创建时间")}</dt>
          <dd>{when(summary.createdAt)}</dd>
          <dt className="text-neutral-500">{t("最后活动")}</dt>
          <dd>{when(summary.updatedAt)}</dd>
        </dl>
      </Section>
      {error ? <div className="text-red-300" role="alert">{t("未能读取运行记录：{value}", { value: error })}</div> : null}

      <Section title={t("运行记录（{count}）", { count: summary.runCount })}>
        {runs === null ? (
          <div className="nx-hint">{t("正在读取…")}</div>
        ) : runs.length === 0 ? (
          <div className="nx-hint">{t("尚无运行记录")}</div>
        ) : (
          <ul className="flex flex-col gap-1.5" aria-label={t("运行记录")}>
            {runs.map((run) => (
              <li key={run.id} className="rounded-md border border-neutral-800 px-2 py-1.5">
                <div className="flex items-center gap-1.5">
                  <span className="font-medium">{runStatusLabel(run.status)}</span>
                  <span className="nx-hint ml-auto">{when(run.createdAt)}</span>
                </div>
                <div className="nx-hint mt-0.5 flex flex-wrap gap-x-2">
                  {run.finishedAt ? <span>{t("用时 {value}", { value: formatDuration(run.finishedAt - run.createdAt) })}</span> : null}
                  <span>{modelOf(run.profileId)}</span>
                  {run.tokensIn || run.tokensOut ? (
                    <span>{t("输入 {input} / 输出 {output} tokens", { input: formatNumber(run.tokensIn), output: formatNumber(run.tokensOut) })}</span>
                  ) : null}
                  {run.retries > 0 ? <span>{t("重试 {count} 次", { count: run.retries })}</span> : null}
                  {run.failures > 0 ? <span className="text-red-300">{t("失败 {count} 次", { count: run.failures })}</span> : null}
                </div>
                {run.error ? <div className="mt-0.5 break-words text-[11px] text-red-300">{run.error}</div> : null}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t("文件变更（{count}）", { count: files.length })}>
        {files.length === 0 ? (
          <div className="nx-hint">{t("没有文件变更")}</div>
        ) : (
          <ul className="flex flex-col gap-0.5" aria-label={t("文件变更")}>
            {files.map((file) => (
              <li key={file.path} className="flex items-center gap-1.5">
                <span className="min-w-0 flex-1 truncate font-mono text-[11px]" title={file.path}>{file.path}</span>
                {file.changes > 1 ? <span className="nx-hint shrink-0">×{file.changes}</span> : null}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t("确认记录（{count}）", { count: approvals?.length ?? 0 })}>
        {approvals === null ? (
          <div className="nx-hint">{t("正在读取…")}</div>
        ) : approvals.length === 0 ? (
          <div className="nx-hint">{t("没有需要确认的操作")}</div>
        ) : (
          <ul className="flex flex-col gap-1" aria-label={t("确认记录")}>
            {approvals.map((approval) => (
              <li key={`${approval.runId}:${approval.callId}`} className="rounded-md border border-neutral-800 px-2 py-1">
                <div className="flex items-center gap-1.5">
                  <span className="font-medium">
                    {approval.kind === "question" ? t("提问") : toolLabel(approval.tool ?? "")}
                  </span>
                  <span className={`ml-auto shrink-0 ${approval.decision === "deny" ? "text-red-300" : approval.decision ? "text-neutral-400" : "text-[var(--nx-fg-warning)]"}`}>
                    {approvalDecisionLabel(approval.decision)}
                  </span>
                </div>
                <div className="mt-0.5 truncate font-mono text-[11px] text-neutral-400" title={approval.question ?? approval.rendered}>
                  {approval.question ?? approval.rendered}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  );
}
