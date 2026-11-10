import { useSnippetRuns } from "./snippetRuns";
import { IconLoader } from "../../ui/icons";
import { t } from "../../i18n";

// Progress and the cancel control of the snippet runs that wait for terminal output.
export function SnippetRunIndicator() {
  const runs = useSnippetRuns();
  return (
    <>
      {runs.map((run) => (
        <div
          key={run.id}
          role="status"
          data-testid="snippet-run-indicator"
          className="pointer-events-auto flex items-start gap-2.5 rounded-lg border border-neutral-700 bg-neutral-800/95 px-3.5 py-2.5 text-left text-xs leading-relaxed text-neutral-100 shadow-[var(--shadow-pop)] backdrop-blur"
        >
          <IconLoader size={14} className="mt-px shrink-0 animate-spin text-blue-300" />
          <div className="min-w-0 flex-1 select-text">
            <div className="truncate">{t("正在执行「{name}」 · 第 {step} / {total} 步", { name: run.name, step: Math.max(run.step, 1), total: run.total })}</div>
            {run.waitingFor !== null && (
              <div className="truncate font-mono text-[11px] opacity-80" title={run.waitingFor}>
                {t("等待输出：{pattern}", { pattern: run.waitingFor })}
              </div>
            )}
          </div>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
            aria-label={t("取消执行「{name}」", { name: run.name })}
            onClick={run.cancel}
          >
            {t("取消")}
          </button>
        </div>
      ))}
    </>
  );
}
