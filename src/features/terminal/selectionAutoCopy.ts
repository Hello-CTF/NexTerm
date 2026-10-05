export interface SelectionAutoCopyOptions {
  isEnabled: () => boolean;
  getSelection: () => string;
  onCopied: (text: string) => void;
  onError: (error: unknown) => void;
  writeText?: (text: string) => Promise<void>;
  delayMs?: number;
}

export interface SelectionAutoCopy {
  notifySelectionChanged: () => void;
  dispose: () => void;
}

function defaultWriteText(text: string): Promise<void> | null {
  const clipboard = typeof navigator !== "undefined" ? navigator.clipboard : undefined;
  if (!clipboard || typeof clipboard.writeText !== "function") return null;
  return clipboard.writeText(text);
}

export function createSelectionAutoCopy(options: SelectionAutoCopyOptions): SelectionAutoCopy {
  const delay = options.delayMs ?? 300;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let lastCopied = "";
  let disposed = false;

  const flush = () => {
    timer = null;
    if (disposed || !options.isEnabled()) return;
    const text = options.getSelection();
    if (!text) {
      lastCopied = "";
      return;
    }
    if (text === lastCopied) return;
    const write = options.writeText ?? defaultWriteText;
    const pending = write(text);
    if (pending === null) {
      options.onError(new Error("当前环境不支持剪贴板 API"));
      return;
    }
    pending.then(
      () => {
        if (disposed) return;
        lastCopied = text;
        options.onCopied(text);
      },
      (error: unknown) => {
        if (disposed) return;
        options.onError(error);
      },
    );
  };

  return {
    notifySelectionChanged() {
      if (disposed) return;
      if (timer !== null) clearTimeout(timer);
      timer = setTimeout(flush, delay);
    },
    dispose() {
      disposed = true;
      if (timer !== null) clearTimeout(timer);
      timer = null;
    },
  };
}
