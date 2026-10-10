import { Component, type ReactNode } from "react";
import { describeError } from "./errorText";

interface ErrorBoundaryProps {
  children: ReactNode;
  title?: string;
  onClose?: () => void;
}

interface ErrorBoundaryState {
  error: unknown;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: unknown): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: unknown): void {
    console.error("[NexTerm] 界面渲染崩溃", error);
  }

  render() {
    const { error } = this.state;
    if (error === null || error === undefined) return this.props.children;
    return (
      <div
        role="alert"
        className="flex h-full flex-col items-center justify-center gap-3 bg-neutral-900 px-6 text-center"
      >
        <div className="text-[13px] font-semibold text-neutral-100">
          {this.props.title ?? "面板发生错误"}
        </div>
        <div className="max-w-md text-xs leading-relaxed text-neutral-500">
          {describeError(error)}
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="nx-btn nx-btn-primary nx-btn-sm"
            onClick={() => this.setState({ error: null })}
          >
            重试
          </button>
          {this.props.onClose && (
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={this.props.onClose}
            >
              关闭标签
            </button>
          )}
        </div>
      </div>
    );
  }
}
