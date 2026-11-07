import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  execBatchApi,
  type ExecBatchHistoryScope,
  type ExecBatchResultRow,
  type ExecBatchRow,
  type ExecBatchStatus,
} from "../../ipc/execBatchApi";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import { describeError } from "../../ui/errorText";
import { t } from "../../i18n";

const PAGE_SIZE = 20;

const BATCH_STATUS_LABELS: Record<string, string> = {
  running: t("执行中"),
  success: t("成功"),
  failed: t("失败"),
  canceled: t("已取消"),
};

const TARGET_STATUS_LABELS: Record<ExecBatchStatus, string> = {
  success: t("成功"),
  exit_error: t("非零退出"),
  transport_error: t("传输错误"),
  timeout: t("超时"),
  canceled: t("已取消"),
};

const ERROR_KIND_LABELS: Record<string, string> = {
  remote_exit: t("远端非零退出"),
  deadline_exceeded: t("执行超时"),
  canceled: t("取消"),
  exit_status_missing: t("缺少退出状态"),
  transport: t("传输错误"),
};

function formatTime(value: number | null): string {
  return value === null ? "—" : new Date(value).toLocaleString();
}

function statusClass(status: string): string {
  if (status === "success") return "text-green-300";
  if (status === "canceled" || status === "running") return "text-neutral-400";
  if (status === "timeout") return "text-amber-300";
  return "text-red-300";
}

function HistoryMatrix({ batch, results }: { batch: ExecBatchRow; results: ExecBatchResultRow[] }) {
  return (
    <div className="mt-3 border-t border-neutral-800/70 pt-3" data-testid="exec-batch-history-matrix">
      <div className="mb-2 flex flex-wrap items-center gap-2 text-[11px]">
        <span className="font-medium text-neutral-300">{t("历史结果汇总")}</span>
        <span className="nx-hint" title={batch.id}>{t("批次")} {batch.id.slice(0, 8)}</span>
        <span className="nx-hint">{t("命令摘要")} {batch.commandHash.slice(0, 12)}…</span>
      </div>
      <div className="overflow-x-auto">
        <table className="nx-table nx-table-cards nx-table-fixed min-w-[720px]">
          <thead>
            <tr>
              <th className="w-1/4">{t("目标")}</th>
              <th className="w-24">{t("状态")}</th>
              <th className="w-20">{t("退出码")}</th>
              <th>{t("开始 / 结束")}</th>
              <th className="w-32">{t("错误分类")}</th>
            </tr>
          </thead>
          <tbody>
            {results.map((result) => (
              <tr key={result.sessionId}>
                <td>
                  <div className="truncate text-neutral-300" title={result.sessionId}>{result.sessionName}</div>
                  <div className="truncate font-mono text-[10px] text-neutral-500">{result.sessionId}</div>
                </td>
                <td data-label={t("状态")} className={statusClass(result.status)}>
                  {TARGET_STATUS_LABELS[result.status as ExecBatchStatus] ?? result.status}
                  {result.truncated && <span className="ml-1 text-[10px] text-amber-300">{t("输出曾截断")}</span>}
                </td>
                <td data-label={t("退出码")} className="nx-mono">{result.exitCode ?? "—"}{result.signal ? ` (${result.signal})` : ""}</td>
                <td className="text-[10px] text-neutral-400">
                  <div>{formatTime(result.startedAt)}</div>
                  <div>{formatTime(result.finishedAt)}</div>
                  <div className="nx-mono text-neutral-500">{result.durationMs} ms</div>
                </td>
                <td data-label={t("错误分类")} className="text-neutral-400">{ERROR_KIND_LABELS[result.errorKind] || result.errorKind || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

type HistorySelection = {
  accountKey: string;
  id: string;
  scope: ExecBatchHistoryScope;
};

export function ExecBatchHistory() {
  const queryClient = useQueryClient();
  const accountID = useAuth((state) => state.user?.id ?? null);
  const accountKey = WEB ? (accountID ?? "logged-out") : "local";
  const previousAccountKey = useRef(accountKey);
  const [open, setOpen] = useState(false);
  const [offset, setOffset] = useState(0);
  const [selection, setSelection] = useState<HistorySelection | null>(null);
  const selected = selection?.accountKey === accountKey ? selection : null;
  useEffect(() => {
    if (previousAccountKey.current === accountKey) return;
    const previous = previousAccountKey.current;
    previousAccountKey.current = accountKey;
    setSelection(null);
    setOffset(0);
    queryClient.removeQueries({ queryKey: ["exec-batch-history", previous] });
    queryClient.removeQueries({ queryKey: ["exec-batch-history-detail", previous] });
  }, [accountKey, queryClient]);
  const listQuery = useQuery({
    queryKey: ["exec-batch-history", accountKey, offset],
    queryFn: () => execBatchApi.historyList({ limit: PAGE_SIZE, offset }),
    enabled: open,
    retry: false,
  });
  const detailQuery = useQuery({
    queryKey: ["exec-batch-history-detail", accountKey, selected?.id ?? "", selected?.scope ?? "self"],
    queryFn: () => execBatchApi.historyGet(selected?.id ?? "", selected?.scope ?? "self"),
    enabled: open && selected !== null,
    retry: false,
  });
  const page = listQuery.isError ? undefined : listQuery.data;
  const detail = detailQuery.isError ? undefined : detailQuery.data;
  const changePage = (nextOffset: number) => {
    setOffset(nextOffset);
    setSelection(null);
  };

  return (
    <div className="border-t border-neutral-800/70 px-3 py-2" aria-label={t("批量执行历史")}>
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className="nx-btn" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
          {t("执行历史")}
        </button>
        {open && (
          <button
            type="button"
            className="nx-btn"
            disabled={listQuery.isFetching || detailQuery.isFetching}
            onClick={() => {
              void listQuery.refetch();
              if (selected !== null) void detailQuery.refetch();
            }}
          >
            {t("刷新")}
          </button>
        )}
        <span className="nx-hint">{t("只记录命令摘要、状态和时间；不保存命令原文、命令输出和原始错误信息。")}</span>
      </div>
      {open && (
        <div className="mt-3">
          {listQuery.isPending && <div className="text-[11px] text-neutral-500">{t("正在加载历史…")}</div>}
          {listQuery.isError && (
            <div role="alert" className="text-[11px] text-red-300">{t("未能加载历史：")}{describeError(listQuery.error)}</div>
          )}
          {page && (
            <>
              <div className="overflow-x-auto">
                <table className="nx-table nx-table-cards min-w-[640px]">
                  <thead>
                    <tr><th>{t("开始时间")}</th><th>{t("状态")}</th><th>{t("目标数")}</th><th>{t("命令摘要")}</th><th>{t("结果")}</th></tr>
                  </thead>
                  <tbody>
                    {page.items.map((batch) => (
                      <tr key={`${batch.id}:${batch.ownerScope}`}>
                        <td data-label={t("开始时间")} className="whitespace-nowrap text-neutral-400">{formatTime(batch.startedAt)}</td>
                        <td data-label={t("状态")} className={statusClass(batch.status)}>
                          {BATCH_STATUS_LABELS[batch.status] ?? batch.status}
                          {batch.production && <span className="ml-1 text-[10px] text-amber-300">{t("生产")}</span>}
                        </td>
                        <td data-label={t("目标数")} className="nx-mono">{batch.targetCount}</td>
                        <td className="nx-mono text-neutral-500" title={batch.commandHash}>{batch.commandHash.slice(0, 12)}…</td>
                        <td>
                          <button
                            type="button"
                            className="nx-btn"
                            onClick={() => setSelection({
                              accountKey,
                              id: batch.id,
                              scope: batch.ownerScope === "ownerless" ? "ownerless" : "self",
                            })}
                          >
                            {t("查看矩阵")}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {page.items.length === 0 && <div className="py-2 text-[11px] text-neutral-500">{t("暂无批量执行历史")}</div>}
              <div className="mt-2 flex items-center gap-2 text-[11px] text-neutral-500">
                <button type="button" className="nx-btn" disabled={offset === 0} onClick={() => changePage(Math.max(0, offset - PAGE_SIZE))}>
                  {t("上一页")}
                </button>
                <button
                  type="button"
                  className="nx-btn"
                  disabled={offset + page.items.length >= page.total}
                  onClick={() => changePage(offset + PAGE_SIZE)}
                >
                  {t("下一页")}
                </button>
                <span>{t("第")} {Math.floor(offset / PAGE_SIZE) + 1} {t("页 · 共")} {page.total} {t("批")}</span>
              </div>
            </>
          )}
          {detailQuery.isPending && <div className="mt-2 text-[11px] text-neutral-500">{t("正在加载结果汇总…")}</div>}
          {detailQuery.isError && (
            <div role="alert" className="mt-2 text-[11px] text-red-300">{t("未能加载结果汇总：")}{describeError(detailQuery.error)}</div>
          )}
          {detail && <HistoryMatrix batch={detail.batch} results={detail.results} />}
        </div>
      )}
    </div>
  );
}
