import { useEffect, useMemo, useRef, useState } from "react";
import {
  createExecBatchId,
  execBatchApi,
  type ExecBatchRequest,
  type ExecBatchResult,
} from "../../ipc/execBatchApi";
import { getProductionChallenge, type ExecBatchProductionChallenge } from "../production/productionChallenge";
import { describeError } from "../../ui/errorText";
import { IconPlay, IconStop } from "../../ui/icons";
import { ProductionConfirmationDialog } from "./ProductionConfirmationDialog";
import { TargetSetPicker, type TargetSetPickerTarget } from "./TargetSetPicker";
import {
  analyzeDriftResults,
  driftDiffLineText,
  driftFileCommand,
  driftTextDiff,
  type DriftAnalysis,
  type DriftGroup,
} from "./drift";

const MAX_TARGETS = 64;
const MAX_DIFF_LINES = 500;

function resultName(result: ExecBatchResult): string {
  return result.sessionName || result.sessionId;
}

function GroupHosts({ group, onPin }: { group: DriftGroup; onPin: (sessionId: string) => void }) {
  return (
    <div>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {group.results.map((result) => (
          <button
            type="button"
            key={result.sessionId}
            className="nx-badge"
            title={`将 ${resultName(result)} 固定为 baseline`}
            onClick={() => onPin(result.sessionId)}
          >
            {resultName(result)}
          </button>
        ))}
      </div>
      {group.results.filter((result) => result.stderr).map((result) => (
        <details key={result.sessionId} className="mt-1 text-[11px]">
          <summary className="cursor-pointer text-neutral-500">{resultName(result)} 的 stderr</summary>
          <pre className="nx-pre mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all">{result.stderr}</pre>
        </details>
      ))}
    </div>
  );
}

export function DriftComparePanel({
  targets,
  metadataError,
}: {
  targets: readonly TargetSetPickerTarget[];
  metadataError?: string | null;
}) {
  const [selected, setSelected] = useState<string[]>([]);
  const [sourceKind, setSourceKind] = useState<"file" | "command">("file");
  const [source, setSource] = useState("/etc/hostname");
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [analysis, setAnalysis] = useState<DriftAnalysis | null>(null);
  const [production, setProduction] = useState<{
    request: ExecBatchRequest;
    challenge: ExecBatchProductionChallenge;
  } | null>(null);
  const [baselineGroupId, setBaselineGroupId] = useState<string | null>(null);
  const [compareGroupId, setCompareGroupId] = useState<string | null>(null);
  const [baselineSessionId, setBaselineSessionId] = useState<string | null>(null);
  const activeRef = useRef<string | null>(null);

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => current.filter((id) => available.has(id)));
  }, [targets]);

  useEffect(
    () => () => {
      const active = activeRef.current;
      activeRef.current = null;
      if (active) void execBatchApi.cancel(active).catch(() => undefined);
    },
    [],
  );

  const selectedIds = targets.filter((target) => selected.includes(target.id)).map((target) => target.id);
  const groups = useMemo(() => analysis?.groups ?? [], [analysis]);
  const baselineGroup = groups.find((group) => group.id === baselineGroupId) ?? groups[0];
  const compareGroup = groups.find((group) => group.id === compareGroupId && group.id !== baselineGroup?.id)
    ?? groups.find((group) => group.id !== baselineGroup?.id);
  const baselineResult = baselineGroup?.results.find((result) => result.sessionId === baselineSessionId);
  const diff = useMemo(() => {
    if (!baselineGroup || !compareGroup || baselineGroup.binary || compareGroup.binary
      || baselineGroup.truncated || compareGroup.truncated) return [];
    return driftTextDiff(baselineGroup.content, compareGroup.content);
  }, [baselineGroup, compareGroup]);

  const run = async (request: ExecBatchRequest) => {
    if (activeRef.current) return;
    activeRef.current = request.batchId;
    setRunning(true);
    setError(null);
    setAnalysis(null);
    setProduction(null);
    try {
      const batch = await execBatchApi.execute(request);
      const next = await analyzeDriftResults(batch.results);
      if (activeRef.current !== request.batchId) return;
      setAnalysis(next);
      setBaselineGroupId(next.groups[0]?.id ?? null);
      setCompareGroupId(next.groups.find((group) => group.id !== next.groups[0]?.id)?.id ?? null);
      setBaselineSessionId(null);
    } catch (cause) {
      if (activeRef.current !== request.batchId) return;
      const challenge = getProductionChallenge(cause, "exec_batch_production_confirmation");
      if (challenge?.batchId === request.batchId) {
        const unconfirmed = { ...request };
        delete unconfirmed.productionConfirmation;
        setProduction({ request: unconfirmed, challenge });
      } else {
        setError(describeError(cause));
      }
    } finally {
      if (activeRef.current === request.batchId) {
        activeRef.current = null;
        setRunning(false);
      }
    }
  };

  const start = () => {
    if (selectedIds.length === 0) {
      setError("请至少选择一个已连接 SSH 会话");
      return;
    }
    if (selectedIds.length > MAX_TARGETS) {
      setError(`每次最多对比 ${MAX_TARGETS} 个目标`);
      return;
    }
    let command: string;
    try {
      command = sourceKind === "file" ? driftFileCommand(source) : source;
    } catch (cause) {
      setError(describeError(cause));
      return;
    }
    if (!command.trim() || /[\r\n\0]/.test(command)) {
      setError("请输入一条单行只读命令或有效文件路径");
      return;
    }
    void run({
      batchId: createExecBatchId(),
      sessionIds: selectedIds,
      command,
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
  };

  const changeSource = (value: string) => {
    setSource(value);
    setProduction(null);
  };

  return (
    <section className="flex flex-col" aria-label="配置与输出漂移对比">
      <form
        className="flex flex-col gap-3 px-3 pb-3"
        onSubmit={(event) => {
          event.preventDefault();
          start();
        }}
      >
        <TargetSetPicker
          targets={targets}
          selectedIds={selected}
          onChange={(ids) => {
            setSelected(ids);
            setProduction(null);
          }}
          disabled={running}
          maxTargets={MAX_TARGETS}
          metadataError={metadataError}
        />
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            对比来源
            <select
              className="nx-select w-32"
              aria-label="漂移来源"
              value={sourceKind}
              disabled={running}
              onChange={(event) => {
                const kind = event.target.value as "file" | "command";
                setSourceKind(kind);
                setSource(kind === "file" ? "/etc/hostname" : "uname -a");
                setProduction(null);
              }}
            >
              <option value="file">文件路径</option>
              <option value="command">只读命令</option>
            </select>
          </label>
          <label className="flex min-w-56 flex-1 flex-col gap-1 text-[11px] text-neutral-500">
            {sourceKind === "file" ? "远端文件路径" : "远端只读命令"}
            <input
              className="nx-input font-mono text-xs"
              aria-label={sourceKind === "file" ? "漂移文件路径" : "漂移只读命令"}
              value={source}
              disabled={running}
              onChange={(event) => changeSource(event.target.value)}
            />
          </label>
          {running ? (
            <button
              type="button"
              className="nx-btn nx-btn-danger"
              onClick={() => {
                const active = activeRef.current;
                if (active) void execBatchApi.cancel(active).catch((cause) => setError(describeError(cause)));
              }}
            >
              <IconStop size={13} /> 取消对比
            </button>
          ) : (
            <button type="submit" className="nx-btn nx-btn-primary" disabled={targets.length === 0}>
              <IconPlay size={13} /> 执行对比
            </button>
          )}
        </div>
        <div className="text-[11px] text-neutral-500">
          文件通过 cat 只读读取；命令请输入只读操作。按 stdout 内容哈希分组，stderr 与失败目标单独展示。
        </div>
        {error && <div role="alert" className="text-[11px] text-red-300">{error}</div>}
        {running && <div role="status" className="text-[11px] text-blue-300">正在读取并分组实时输出…</div>}
      </form>

      {production && (
        <ProductionConfirmationDialog
          challenge={production.challenge}
          contentHash={production.challenge.commandHash}
          command={production.request.command}
          onCancel={() => setProduction(null)}
          onConfirm={(environmentNames) => void run({
            ...production.request,
            productionConfirmation: {
              confirmed: true,
              targetSetHash: production.challenge.targetSetHash,
              commandHash: production.challenge.commandHash,
              ...(environmentNames ? { environmentNames } : {}),
            },
          })}
        />
      )}

      {analysis && (
        <div className="border-t border-neutral-800/70 px-3 py-3" data-testid="drift-results">
          <div className="mb-3 flex flex-wrap items-center gap-2 text-[11px]">
            <span className="font-medium text-neutral-300">内容分组</span>
            <span className="nx-hint">
              {groups.length} 组 · {analysis.failures.length} 个失败目标
            </span>
            {baselineResult && <span className="nx-badge nx-badge-blue">baseline：{resultName(baselineResult)}</span>}
          </div>
          {groups.length === 0 && <div className="text-[11px] text-neutral-500">没有可用于对比的成功输出。</div>}
          <div className="flex flex-col gap-2">
            {groups.map((group, index) => (
              <article key={group.id} className="rounded border border-neutral-800/70 p-2.5" data-group-id={group.id}>
                <div className="flex flex-wrap items-center gap-2 text-[11px]">
                  <span className="font-medium text-neutral-200">组 {index + 1} · {group.results.length} 台</span>
                  <span className="nx-mono text-[10px] text-neutral-500" title={group.hash}>SHA-256 {group.hash.slice(0, 12)}…</span>
                  {group.truncated && <span className="nx-badge nx-badge-amber">输出截断，仅返回部分可比较</span>}
                  {group.binary && <span className="nx-badge nx-badge-amber">二进制，不做文本 diff</span>}
                  <span className="nx-spacer" />
                  <button
                    type="button"
                    className="nx-link"
                    disabled={group.id === baselineGroup?.id}
                    onClick={() => {
                      setBaselineGroupId(group.id);
                      setBaselineSessionId(null);
                      if (compareGroupId === group.id) setCompareGroupId(groups.find((item) => item.id !== group.id)?.id ?? null);
                    }}
                  >
                    设为基准组
                  </button>
                  <button
                    type="button"
                    className="nx-link"
                    disabled={group.id === baselineGroup?.id || group.id === compareGroup?.id}
                    onClick={() => setCompareGroupId(group.id)}
                  >
                    对比此组
                  </button>
                </div>
                <GroupHosts group={group} onPin={(sessionId) => {
                  setBaselineGroupId(group.id);
                  setBaselineSessionId(sessionId);
                  if (compareGroupId === group.id) setCompareGroupId(groups.find((item) => item.id !== group.id)?.id ?? null);
                }} />
                {group.binary ? (
                  <div className="mt-2 text-[11px] text-neutral-500">二进制输出 {group.content.length} 个字符；仅提供分组。</div>
                ) : (
                  <details className="mt-2">
                    <summary className="cursor-pointer text-[11px] text-neutral-500">查看本组输出</summary>
                    <pre className="nx-pre mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-all">
                      {group.content || "（空输出）"}
                    </pre>
                  </details>
                )}
              </article>
            ))}
          </div>

          {baselineGroup && compareGroup && (
            <div className="mt-4 border-t border-neutral-800/70 pt-3" data-testid="drift-diff">
              <div className="mb-2 text-[11px] font-medium text-neutral-300">
                组间 diff：{baselineResult ? resultName(baselineResult) : "基准组"} → 对比组
              </div>
              {baselineGroup.truncated || compareGroup.truncated ? (
                <div className="text-[11px] text-amber-300">至少一组输出被截断，不能据此判断完整内容差异。</div>
              ) : baselineGroup.binary || compareGroup.binary ? (
                <div className="text-[11px] text-amber-300">二进制输出不支持行级 diff。</div>
              ) : (
                <>
                  <div className="nx-pre max-h-96 overflow-auto whitespace-pre-wrap break-all font-mono text-[11px]" aria-label="组间行级差异">
                    {diff.slice(0, MAX_DIFF_LINES).map((line, index) => (
                      <div key={index} className={line.kind === "add" ? "text-green-300" : line.kind === "del" ? "text-red-300" : "text-neutral-500"}>
                        {driftDiffLineText(line)}
                      </div>
                    ))}
                  </div>
                  {diff.length > MAX_DIFF_LINES && (
                    <div className="mt-1 text-[10px] text-neutral-500">仅显示前 {MAX_DIFF_LINES} 行 diff，共 {diff.length} 行。</div>
                  )}
                </>
              )}
            </div>
          )}

          {analysis.failures.length > 0 && (
            <div className="mt-4 border-t border-neutral-800/70 pt-3">
              <div className="mb-2 text-[11px] font-medium text-neutral-300">未纳入分组的目标</div>
              <table className="nx-table">
                <tbody>
                  {analysis.failures.map((result) => (
                    <tr key={result.sessionId}>
                      <td>{resultName(result)}</td>
                      <td className="text-red-300">{result.status}</td>
                      <td className="whitespace-pre-wrap break-all text-neutral-400">
                        {result.error || result.stderr || "命令未成功完成"}
                        {result.truncated && "（输出截断）"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
