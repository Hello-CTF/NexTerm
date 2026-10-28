import { Component, Suspense, isValidElement, type ReactNode } from "react";
import { PanelFallback } from "./PanelFallback";

const CHUNK_ERROR_PATTERN =
  /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|Loading (CSS )?chunk [^ ]+ failed|ChunkLoadError/i;

export function isChunkLoadError(error: unknown): boolean {
  if (error instanceof Error && error.name === "ChunkLoadError") return true;
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  return CHUNK_ERROR_PATTERN.test(message);
}

interface ChunkBoundaryProps {
  children: ReactNode;
  onReload?: () => void;
  // Background features render nothing (and only warn) when their chunk fails to load.
  silent?: boolean;
  // Floating UI (dialogs): show the failure in a fixed overlay instead of inside the layout flow.
  overlay?: boolean;
  // A change of this value clears a previous failure (the slot now shows a different lazy component).
  resetKey?: unknown;
}

interface ChunkBoundaryState {
  error: unknown;
}

function childIdentity(children: ReactNode): unknown {
  return isValidElement(children) ? children.type : children;
}

// Only chunk-load failures are handled here; every other error propagates to the surrounding ErrorBoundary.
// React.lazy caches a rejected import, so recovery needs a full reload (also picks up a newer deploy).
export class ChunkLoadBoundary extends Component<ChunkBoundaryProps, ChunkBoundaryState> {
  state: ChunkBoundaryState = { error: null };

  static getDerivedStateFromError(error: unknown): ChunkBoundaryState {
    return { error };
  }

  componentDidCatch(error: unknown): void {
    if (!isChunkLoadError(error)) return;
    if (this.props.silent) console.warn("[NexTerm] background chunk failed to load", error);
    else console.error("[NexTerm] lazy chunk failed to load", error);
  }

  // A slot that now shows a different lazy component must not keep the previous component's failure.
  componentDidUpdate(prev: ChunkBoundaryProps): void {
    if (this.state.error !== null && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null });
    }
  }

  render() {
    const { error } = this.state;
    if (error === null || error === undefined) return this.props.children;
    if (!isChunkLoadError(error)) throw error;
    if (this.props.silent) return null;
    const alert = (
      <div role="alert" className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
        <div className="text-[13px] font-semibold text-neutral-100">未能加载界面模块</div>
        <div className="max-w-md text-xs leading-relaxed text-neutral-500">
          网络中断或应用已更新，无法下载该模块。请重新加载页面后重试。
        </div>
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm"
          onClick={() => (this.props.onReload ? this.props.onReload() : window.location.reload())}
        >
          重新加载
        </button>
      </div>
    );
    if (!this.props.overlay) return alert;
    return (
      <div className="fixed inset-0 z-[90] flex items-center justify-center bg-black/50 p-6">
        <div className="max-h-full rounded-lg border border-neutral-700 bg-neutral-900 py-6">{alert}</div>
      </div>
    );
  }
}

// Lazy wraps lazily loaded UI: "panel" shows the delayed skeleton, "none" renders nothing while loading.
export function Lazy({
  children,
  fallback = "panel",
  silent = false,
  overlay = false,
}: {
  children: ReactNode;
  fallback?: "panel" | "none";
  silent?: boolean;
  overlay?: boolean;
}) {
  return (
    <ChunkLoadBoundary silent={silent} overlay={overlay} resetKey={childIdentity(children)}>
      <Suspense fallback={fallback === "panel" ? <PanelFallback /> : null}>{children}</Suspense>
    </ChunkLoadBoundary>
  );
}
