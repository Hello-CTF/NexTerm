// 批量执行命令（多选 → 参数 → 逐台确认 → 矩阵结果）。
//
// 定位是「一次在几台机器上跑同一条只读/巡检命令」——不是自动化编排平台：
// 没有变量替换、没有依赖、没有"危险命令特殊照顾"（也就没有「一键 rm」这种东西），
// 失败也不静默重试。并发与超时都交给内核统一调度（见 ipc/commands.ts 的 batchApi）。
//
// 参与范围与「导出到剪贴板」同一判据：ssh / docker / winrm 且已填 host。
// local 是本机、mysql / redis 是数据库连接串语义，没有"在远端 shell 里执行"的概念，
// 因此不参与，并在界面上说明理由。
import { Fragment, useMemo, useState } from "react";
import { batchApi, type Asset, type BatchExecRow } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  assetIcon,
  IconAlert,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconCopy,
  IconLoader,
  IconPlay,
} from "../../ui/icons";

/** 能在远端跑 shell 的资产类型（与导出同一判据）。 */
const EXEC_KINDS = new Set(["ssh", "docker", "winrm"]);

function clamp(v: number, lo: number, hi: number, fallback: number): number {
  if (!Number.isFinite(v)) return fallback;
  return Math.min(hi, Math.max(lo, Math.round(v)));
}

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

export function BatchExecModal({
  assets,
  onClose,
}: {
  assets: Asset[];
  onClose: () => void;
}) {
  const { pushToast } = useUi();
  const list = useMemo(
    () => assets.filter((a) => EXEC_KINDS.has(a.kind) && !!a.host),
    [assets],
  );

  const [stage, setStage] = useState<"setup" | "confirm" | "running" | "result">("setup");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [command, setCommand] = useState("");
  const [timeoutSec, setTimeoutSec] = useState(30);
  const [concurrency, setConcurrency] = useState(6);
  const [results, setResults] = useState<BatchExecRow[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const allPicked = list.length > 0 && list.every((a) => picked.has(a.id));
  const pickedAssets = list.filter((a) => picked.has(a.id));

  const toggle = (id: string, on: boolean) => {
    setPicked((cur) => {
      const next = new Set(cur);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const exec = async () => {
    const ids = pickedAssets.map((a) => a.id);
    if (ids.length === 0 || !command.trim()) return;
    setStage("running");
    try {
      const rows =
        (await batchApi.exec({
          assetIds: ids,
          command,
          timeoutMs: clamp(timeoutSec, 1, 3600, 30) * 1000,
          concurrency: clamp(concurrency, 1, 16, 6),
        })) ?? [];
      setResults(rows);
      setStage("result");
    } catch (e) {
      pushToast("error", `批量执行失败：${describeError(e)}`);
      setStage("setup");
    }
  };

  const okCount = results.filter((r) => r.ok).length;

  const copyAll = async () => {
    const text = [
      `# 批量执行结果 · 命令：${command}`,
      `# 成功 ${okCount} / ${results.length}`,
      "",
      ...results.map((r) => {
        const head = r.ok
          ? `[OK] ${r.name} (${r.host}) exit=${r.exitCode ?? 0} ${fmtDuration(r.durationMs)}`
          : `[FAIL] ${r.name} (${r.host}) ${r.error ?? `exit=${r.exitCode}`} ${fmtDuration(r.durationMs)}`;
        const out = r.stdout.trim();
        const err = r.stderr.trim();
        return [head, out && `  stdout:\n${out}`, err && `  stderr:\n${err}`]
          .filter(Boolean)
          .join("\n");
      }),
    ].join("\n");
    try {
      await navigator.clipboard.writeText(text);
      pushToast("success", "已复制全部执行结果到剪贴板");
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  const title =
    stage === "setup"
      ? "批量执行命令"
      : stage === "confirm"
        ? "确认执行"
        : stage === "running"
          ? "执行中…"
          : "执行结果";

  return (
    <div className="nx-overlay" onClick={stage === "running" ? undefined : onClose}>
      <div className="nx-modal max-w-[820px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">{title}</span>
          {stage === "result" && (
            <span className="nx-badge nx-badge-green">
              {okCount}/{results.length} 成功
            </span>
          )}
          <div className="nx-spacer" />
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="关闭"
            onClick={onClose}
            disabled={stage === "running"}
          >
            <IconClose size={14} />
          </button>
        </div>

        {stage === "setup" && (
          <div className="nx-modal-body">
            {/* 工具条：全选 + 选中计数 */}
            <div className="mb-2 flex items-center gap-2">
              <label className="flex cursor-pointer items-center gap-2 text-[12px] text-neutral-400">
                <input
                  className="nx-check"
                  type="checkbox"
                  aria-label="全选可执行资产"
                  checked={allPicked}
                  ref={(el) => {
                    if (el) el.indeterminate = picked.size > 0 && !allPicked;
                  }}
                  onChange={(e) =>
                    setPicked(e.target.checked ? new Set(list.map((a) => a.id)) : new Set())
                  }
                />
                全选
              </label>
              <div className="nx-spacer" />
              <span className="text-[11.5px] text-neutral-500">
                已选 <span className="text-neutral-300">{picked.size}</span> / {list.length}
              </span>
            </div>

            <div className="max-h-[34vh] overflow-y-auto rounded-lg border border-neutral-800/70 bg-neutral-950/40 p-1">
              {list.length === 0 ? (
                <div className="nx-hint px-3 py-8 text-center">
                  没有可批量执行的资产
                  <br />
                  仅支持已填主机地址的 SSH / Docker / WinRM 资产
                </div>
              ) : (
                list.map((a) => {
                  const Icon = assetIcon(a.kind);
                  const on = picked.has(a.id);
                  const endpoint = `${a.username ? `${a.username}@` : ""}${a.host}${
                    a.port ? `:${a.port}` : ""
                  }`;
                  return (
                    <label
                      key={a.id}
                      className={`flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 ${
                        on ? "bg-blue-500/[.08]" : "hover:bg-white/[.04]"
                      }`}
                    >
                      <input
                        className="nx-check"
                        type="checkbox"
                        aria-label={`选择 ${a.name}`}
                        checked={on}
                        onChange={(e) => toggle(a.id, e.target.checked)}
                      />
                      <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-white/[.06] text-neutral-400">
                        <Icon size={13} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[12.5px] text-neutral-200">
                          {a.name}
                        </span>
                        <span className="nx-mono block truncate text-[11px] text-neutral-500">
                          {endpoint}
                        </span>
                      </span>
                      <span className="nx-badge shrink-0">{a.kind}</span>
                    </label>
                  );
                })
              )}
            </div>

            <div className="mt-3">
              <label className="nx-label">命令</label>
              <textarea
                className="nx-textarea nx-mono text-[11.5px]"
                rows={3}
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                placeholder="例如：uptime && df -h"
              />
            </div>

            <div className="mt-2 flex gap-3">
              <div className="w-[130px]">
                <label className="nx-label">超时（秒）</label>
                <input
                  type="number"
                  className="nx-input"
                  min={1}
                  max={3600}
                  value={timeoutSec}
                  onChange={(e) => setTimeoutSec(Number(e.target.value))}
                />
              </div>
              <div className="w-[130px]">
                <label className="nx-label">并发（1–16）</label>
                <input
                  type="number"
                  className="nx-input"
                  min={1}
                  max={16}
                  value={concurrency}
                  onChange={(e) => setConcurrency(Number(e.target.value))}
                />
              </div>
            </div>

            <div className="nx-hint mt-2">
              本机 / MySQL / Redis 资产不参与：前者没有远端 shell，后者是数据库连接串语义。
              并发与超时由内核统一调度，失败不重试。
            </div>
          </div>
        )}

        {stage === "confirm" && (
          <div className="nx-modal-body">
            <div className="mb-3 flex items-start gap-2 rounded-md border border-amber-500/25 bg-amber-500/10 px-2.5 py-2 text-[11.5px] text-amber-300/90">
              <IconAlert size={13} className="mt-[1px] shrink-0" />
              <span>
                即将在下列 <span className="font-semibold">{pickedAssets.length}</span> 台主机上
                执行同一条命令，请逐台确认无误。
              </span>
            </div>

            <label className="nx-label">命令</label>
            <pre className="nx-mono mb-3 max-h-[18vh] overflow-auto rounded-lg border border-neutral-800/70 bg-neutral-950/40 px-3 py-2 text-[11.5px] text-neutral-200">
              {command}
            </pre>

            <label className="nx-label">目标主机</label>
            <div className="max-h-[30vh] overflow-y-auto rounded-lg border border-neutral-800/70 bg-neutral-950/40 p-1">
              {pickedAssets.map((a) => {
                const Icon = assetIcon(a.kind);
                return (
                  <div key={a.id} className="flex items-center gap-2.5 rounded-md px-2 py-1.5">
                    <Icon size={13} className="shrink-0 text-neutral-500" />
                    <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200">
                      {a.name}
                    </span>
                    <span className="nx-mono shrink-0 text-[11px] text-neutral-500">
                      {a.username ? `${a.username}@` : ""}
                      {a.host}
                      {a.port ? `:${a.port}` : ""}
                    </span>
                  </div>
                );
              })}
            </div>

            <div className="nx-hint mt-2">
              超时 {clamp(timeoutSec, 1, 3600, 30)} 秒 · 并发 {clamp(concurrency, 1, 16, 6)}
            </div>
          </div>
        )}

        {stage === "running" && (
          <div className="nx-modal-body">
            <div className="flex items-center justify-center gap-2 py-10 text-[12.5px] text-neutral-400">
              <IconLoader size={16} className="animate-spin" />
              正在 {pickedAssets.length} 台主机上执行…
            </div>
          </div>
        )}

        {stage === "result" && (
          <div className="nx-modal-body">
            <div className="max-h-[52vh] overflow-y-auto rounded-lg border border-neutral-800/70 bg-neutral-950/40">
              <table className="nx-table nx-table-fixed">
                <thead>
                  <tr>
                    <th style={{ width: 30 }} />
                    <th style={{ width: 170 }}>主机</th>
                    <th style={{ width: 70 }}>状态</th>
                    <th style={{ width: 70 }} className="nx-right">
                      退出码
                    </th>
                    <th style={{ width: 80 }} className="nx-right">
                      耗时
                    </th>
                    <th>输出摘要</th>
                  </tr>
                </thead>
                <tbody>
                  {results.map((r) => {
                    const open = expanded.has(r.assetId);
                    const firstLine =
                      (r.stdout.trim().split("\n")[0] || r.stderr.trim().split("\n")[0] || r.error || "")
                        .slice(0, 120) || "（无输出）";
                    return (
                      <Fragment key={r.assetId}>
                        <tr
                          className={r.ok ? "" : "bg-red-500/[.06]"}
                          style={{ cursor: "pointer" }}
                          onClick={() =>
                            setExpanded((cur) => {
                              const next = new Set(cur);
                              if (next.has(r.assetId)) next.delete(r.assetId);
                              else next.add(r.assetId);
                              return next;
                            })
                          }
                        >
                          <td className="text-neutral-500">
                            {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
                          </td>
                          <td className="truncate">
                            {r.name}
                            <span className="nx-mono ml-1.5 text-neutral-500">{r.host}</span>
                          </td>
                          <td>
                            {r.ok ? (
                              <span className="nx-badge nx-badge-green">成功</span>
                            ) : (
                              <span className="nx-badge nx-badge-red">失败</span>
                            )}
                          </td>
                          <td className="nx-right nx-mono">{r.exitCode ?? "—"}</td>
                          <td className="nx-right nx-mono">{fmtDuration(r.durationMs)}</td>
                          <td className="nx-mono truncate">{firstLine}</td>
                        </tr>
                        {open && (
                          <tr>
                            <td />
                            <td colSpan={5}>
                              {r.error && (
                                <div className="mb-1.5 text-[11.5px] text-red-300">{r.error}</div>
                              )}
                              {r.stdout.trim() && (
                                <>
                                  <div className="mb-0.5 text-[11px] text-neutral-500">stdout</div>
                                  <pre className="nx-mono mb-2 max-h-[24vh] overflow-auto whitespace-pre-wrap break-all rounded border border-neutral-800/70 bg-neutral-950/60 px-2 py-1.5 text-[11px] text-neutral-300">
                                    {r.stdout}
                                  </pre>
                                </>
                              )}
                              {r.stderr.trim() && (
                                <>
                                  <div className="mb-0.5 text-[11px] text-neutral-500">stderr</div>
                                  <pre className="nx-mono max-h-[24vh] overflow-auto whitespace-pre-wrap break-all rounded border border-neutral-800/70 bg-neutral-950/60 px-2 py-1.5 text-[11px] text-red-300/90">
                                    {r.stderr}
                                  </pre>
                                </>
                              )}
                              {!r.error && !r.stdout.trim() && !r.stderr.trim() && (
                                <div className="text-[11.5px] text-neutral-500">（无输出）</div>
                              )}
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        <div className="nx-modal-footer">
          {stage === "setup" && (
            <>
              <button className="nx-btn nx-btn-ghost" onClick={onClose}>
                取消
              </button>
              <button
                className="nx-btn nx-btn-primary"
                disabled={picked.size === 0 || !command.trim()}
                onClick={() => setStage("confirm")}
              >
                下一步
              </button>
            </>
          )}
          {stage === "confirm" && (
            <>
              <button className="nx-btn nx-btn-ghost" onClick={() => setStage("setup")}>
                返回
              </button>
              <button className="nx-btn nx-btn-primary" onClick={() => void exec()}>
                <IconPlay size={12} />
                确认执行
              </button>
            </>
          )}
          {stage === "result" && (
            <>
              <button
                className="nx-btn nx-btn-ghost"
                onClick={() => {
                  setResults([]);
                  setExpanded(new Set());
                  setStage("setup");
                }}
              >
                再执行一次
              </button>
              <button className="nx-btn nx-btn-primary" onClick={() => void copyAll()}>
                <IconCopy size={12} />
                复制全部结果
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
