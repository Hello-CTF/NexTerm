import { useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useUi } from "../app/store";
import { isMac } from "../app/platform";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "./DialogHost";

export function PromptModal() {
  const prompt = useUi((state) => state.textPrompt);
  const closeTextPrompt = (result: string | null) => {
    if (!prompt || useUi.getState().textPrompt !== prompt) return;
    useUi.setState({ textPrompt: null });
    prompt.resolve(result);
  };
  const [value, setValue] = useState(() => prompt?.value ?? "");
  const [previousPrompt, setPreviousPrompt] = useState(prompt);
  if (prompt !== previousPrompt) {
    setPreviousPrompt(prompt);
    if (prompt) setValue(prompt.value);
  }
  const inputRef = useRef<HTMLInputElement | HTMLTextAreaElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const composingRef = useRef(false);
  const messageId = useId();
  const hintId = useId();
  const multiLine = !!prompt && !prompt.secret && (prompt.multiLine ?? (prompt.message.includes("\n") || prompt.value.length > 60));
  const layer = useOverlayFocus(prompt, modalRef, {
    initialFocus: () => inputRef.current,
    selectInitial: !!prompt && !multiLine,
  });

  if (!prompt) return null;

  const settle = (result: string | null) => {
    if (useUi.getState().textPrompt === prompt) closeTextPrompt(result);
  };
  const submit = () => {
    if (!composingRef.current) settle(value);
  };
  const shortcut = isMac() ? "⌘+Enter" : "Ctrl+Enter";
  const hint = multiLine ? `${shortcut} 提交，Esc 取消` : "Enter 提交，Esc 取消";
  const onInputKeyDown = (event: ReactKeyboardEvent<HTMLInputElement | HTMLTextAreaElement>) => {
    if (isImeKeyEvent(event)) return;
    if (
      event.key === "Enter" &&
      !event.repeat &&
      (!multiLine || event.ctrlKey || event.metaKey)
    ) {
      event.preventDefault();
      submit();
    }
  };
  const compositionProps = {
    onCompositionStart: () => {
      composingRef.current = true;
    },
    onCompositionEnd: () => {
      composingRef.current = false;
    },
  };

  return (
    <div className="nx-overlay" onClick={() => settle(null)}>
      <div
        ref={modalRef}
        className={`nx-modal ${multiLine ? "max-w-[620px]" : "max-w-[440px]"}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={messageId}
        aria-describedby={hintId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (!layer.isTopmost()) return;
          if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
            event.preventDefault();
            settle(null);
            return;
          }
          trapOverlayTab(event, modalRef.current);
        }}
      >
        <div className="nx-modal-body">
          <label id={messageId} htmlFor={multiLine ? `${messageId}-textarea` : `${messageId}-input`} className="mb-3 block whitespace-pre-wrap text-[12.5px] leading-relaxed text-neutral-200">
            {prompt.message}
          </label>
          {multiLine ? (
            <textarea
              id={`${messageId}-textarea`}
              ref={(element) => {
                inputRef.current = element;
              }}
              rows={9}
              className="nx-textarea nx-textarea-grow"
              aria-describedby={hintId}
              value={value}
              onChange={(event) => setValue(event.target.value)}
              onKeyDown={onInputKeyDown}
              {...compositionProps}
            />
          ) : (
            <input
              id={`${messageId}-input`}
              ref={(element) => {
                inputRef.current = element;
              }}
              type={prompt.secret ? "password" : "text"}
              className="nx-input"
              value={value}
              autoComplete="off"
              aria-describedby={hintId}
              onChange={(event) => setValue(event.target.value)}
              onKeyDown={onInputKeyDown}
              {...compositionProps}
            />
          )}
          <div id={hintId} className="nx-prompt-hint">{hint}</div>
        </div>
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost" onClick={() => settle(null)}>
            取消
          </button>
          <button type="button" className="nx-btn nx-btn-primary" onClick={submit}>
            确定
          </button>
        </div>
      </div>
    </div>
  );
}
