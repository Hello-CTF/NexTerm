import { sanitizeWorkspaceProblemText, useUi, type WorkspaceProblem } from "../../app/store";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";

export function workspaceProblemCopyText(problem: WorkspaceProblem): string {
  const lines = [
    `时间：${new Date(problem.createdAt).toLocaleString()}`,
    `操作：${problem.operation}`,
    `目标：${problem.target.title} · ${problem.target.identity}`,
  ];
  if (problem.target.schema !== undefined) lines.push(`schema：${problem.target.schema || "默认"}`);
  lines.push(`摘要：${problem.summary}`, `详情：${problem.detail}`);
  return sanitizeWorkspaceProblemText(lines.join("\n"));
}

interface WorkspaceProblemActions {
  onRetryRead?: (problem: WorkspaceProblem) => void;
  onLoadSql?: (problem: WorkspaceProblem) => void;
  onReconnect?: (problem: WorkspaceProblem) => void;
  targetAvailable?: (problem: WorkspaceProblem) => boolean;
}

export function WorkspaceProblems({
  workspaceId,
  ...actions
}: WorkspaceProblemActions & { workspaceId: string }) {
  const problems = useUi((state) => state.workspaceRuntime[workspaceId]?.problems);
  if (!problems || problems.length === 0) return null;
  return <WorkspaceProblemSection workspaceId={workspaceId} problems={problems} {...actions} />;
}

export function ConnectionProblemNotice({
  problem,
  ...actions
}: WorkspaceProblemActions & { problem: WorkspaceProblem }) {
  return <WorkspaceProblemSection problems={[problem]} {...actions} />;
}

function WorkspaceProblemSection({
  workspaceId,
  problems,
  onRetryRead,
  onLoadSql,
  onReconnect,
  targetAvailable,
}: WorkspaceProblemActions & {
  workspaceId?: string;
  problems: WorkspaceProblem[];
}) {
  const { pushToast } = useUi();

  const copy = async (problem: WorkspaceProblem) => {
    try {
      await copyText(workspaceProblemCopyText(problem));
      pushToast("success", "问题详情已复制");
    } catch (error) {
      pushToast("error", `复制失败：${describeError(error)}`);
    }
  };

  const dismiss = (problem: WorkspaceProblem) => {
    if (problem.connectionKey) {
      useUi.getState().clearConnectionProblem(problem.connectionKey);
    } else if (workspaceId) {
      useUi.getState().dismissWorkspaceProblem(workspaceId, problem.id, problem.generation);
    }
  };

  return (
    <section aria-label="本次问题" className="border-b border-neutral-800/60 bg-neutral-950/20">
      <header className="flex items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <h3 className="text-xs font-medium text-neutral-300">本次问题</h3>
        <span className="nx-hint">仅保留最近 50 条</span>
      </header>
      <ul aria-label="问题列表" className="divide-y divide-neutral-800/60">
        {problems.map((problem) => {
          const available = targetAvailable?.(problem) ?? true;
          return (
            <li key={problem.id}>
              <article className="flex flex-col gap-1.5 px-3 py-2">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-neutral-500">
                  <time dateTime={new Date(problem.createdAt).toISOString()}>
                    {new Date(problem.createdAt).toLocaleString()}
                  </time>
                  <span>{problem.operation}</span>
                  <span>
                    {problem.target.title} · {problem.target.identity}
                    {problem.target.kind ? ` · ${problem.target.kind}` : ""}
                  </span>
                  {problem.target.schema !== undefined && <span>schema {problem.target.schema || "默认"}</span>}
                </div>
                <div className="text-xs text-red-300">{problem.summary}</div>
                {problem.outcome === "unknown" && (
                  <div className="text-[11px] text-amber-300">结果未知，请先确认数据库状态</div>
                )}
                {!available && <div className="text-[11px] text-neutral-500">原目标不可用，仅可查看或复制</div>}
                {problem.sqlTruncated && <div className="text-[11px] text-amber-300">SQL 副本不完整</div>}
                {problem.detailTruncated && <div className="text-[11px] text-amber-300">详情已截断</div>}
                <details className="text-[11px] text-neutral-400">
                  <summary className="cursor-pointer select-none">查看详情</summary>
                  <pre className="nx-pre mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-all">
                    {problem.detail}
                  </pre>
                </details>
                <div className="flex flex-wrap items-center gap-2">
                  <button type="button" className="nx-link" onClick={() => void copy(problem)}>
                    复制
                  </button>
                  {available && problem.canRetryRead && onRetryRead && (
                    <button type="button" className="nx-link" onClick={() => onRetryRead(problem)}>
                      重试
                    </button>
                  )}
                  {available && problem.sql !== undefined && onLoadSql && (
                    <button type="button" className="nx-link" onClick={() => onLoadSql(problem)}>
                      {problem.outcome === "unknown" ? "重新发起" : "载入 SQL"}
                    </button>
                  )}
                  {available && problem.connection && onReconnect && (
                    <button type="button" className="nx-link" onClick={() => onReconnect(problem)}>
                      重新连接
                    </button>
                  )}
                  <button
                    type="button"
                    className="nx-link ml-auto"
                    aria-label={`关闭问题：${problem.summary}`}
                    onClick={() => dismiss(problem)}
                  >
                    关闭
                  </button>
                </div>
              </article>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
