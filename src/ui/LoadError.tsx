import { friendlyErrorDetail, friendlyErrorMessage } from "./friendlyError";
import { IconRefresh, IconXCircle } from "./icons";

// Plain-language load failure with a retry; the raw error is only shown under the details disclosure.
export function LoadError({ title, error, onRetry }: { title: string; error: unknown; onRetry: () => void }) {
  return (
    <div role="alert" className="px-2 py-6 text-center">
      <div className="flex items-center justify-center gap-1.5 text-[12px] text-red-400">
        <IconXCircle size={13} /> {title}
      </div>
      <div className="nx-hint mt-1.5">{friendlyErrorMessage(error)}</div>
      <button type="button" className="nx-btn nx-btn-outline nx-btn-sm mt-3" onClick={onRetry}>
        <IconRefresh size={12} />
        重试
      </button>
      <details className="mt-2 text-left text-[11px] text-neutral-500">
        <summary className="cursor-pointer text-center">查看详情</summary>
        <pre className="mt-1 whitespace-pre-wrap break-all">{friendlyErrorDetail(error)}</pre>
      </details>
    </div>
  );
}
