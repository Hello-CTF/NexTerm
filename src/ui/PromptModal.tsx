// 全局文本输入弹窗（替代 window.prompt，Tauri 下不可用）。
import { useEffect, useRef, useState } from "react";
import { useUi } from "../app/store";

export function PromptModal() {
  const prompt = useUi((s) => s.textPrompt);
  const closeTextPrompt = useUi((s) => s.closeTextPrompt);
  const [value, setValue] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (prompt) {
      setValue(prompt.value);
      setTimeout(() => inputRef.current?.focus(), 30);
    }
  }, [prompt]);

  if (!prompt) return null;
  const submit = () => closeTextPrompt(value);

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/50">
      <div className="w-96 rounded-lg border border-neutral-700 bg-neutral-900 p-4 shadow-xl">
        <div className="mb-2 text-sm text-neutral-200">{prompt.message}</div>
        <input
          ref={inputRef}
          className="mb-3 w-full rounded bg-neutral-800 px-2 py-1 text-sm outline-none focus:ring-1 focus:ring-blue-500"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
            if (e.key === "Escape") closeTextPrompt(null);
          }}
        />
        <div className="flex justify-end gap-2">
          <button
            className="rounded px-3 py-1 text-sm text-neutral-400 hover:bg-neutral-800"
            onClick={() => closeTextPrompt(null)}
          >
            取消
          </button>
          <button
            className="rounded bg-blue-600 px-3 py-1 text-sm text-white hover:bg-blue-500"
            onClick={submit}
          >
            确定
          </button>
        </div>
      </div>
    </div>
  );
}
