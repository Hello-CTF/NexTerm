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
      const t = window.setTimeout(() => {
        inputRef.current?.focus();
        inputRef.current?.select();
      }, 30);
      return () => window.clearTimeout(t);
    }
  }, [prompt]);

  if (!prompt) return null;
  const submit = () => closeTextPrompt(value);

  const multiLine = prompt.message.includes("\n") || prompt.value.length > 60;

  return (
    <div className="nx-overlay z-[60]" onClick={() => closeTextPrompt(null)}>
      <div className="nx-modal max-w-[440px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-body">
          <div className="mb-3 text-[12.5px] leading-relaxed whitespace-pre-wrap text-neutral-200">
            {prompt.message}
          </div>
          {multiLine ? (
            <textarea
              autoFocus
              rows={4}
              className="nx-textarea"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) submit();
                if (e.key === "Escape") closeTextPrompt(null);
              }}
            />
          ) : (
            <input
              ref={inputRef}
              className="nx-input"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") submit();
                if (e.key === "Escape") closeTextPrompt(null);
              }}
            />
          )}
        </div>
        <div className="nx-modal-footer">
          <button className="nx-btn nx-btn-ghost" onClick={() => closeTextPrompt(null)}>
            取消
          </button>
          <button className="nx-btn nx-btn-primary" onClick={submit}>
            确定
          </button>
        </div>
      </div>
    </div>
  );
}
