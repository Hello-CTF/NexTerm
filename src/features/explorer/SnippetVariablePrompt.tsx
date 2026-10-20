import {
  useCallback,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconXCircle } from "../../ui/icons";
import type { SnippetVariable, SnippetVariableValues } from "./snippetVariables";

export function SnippetVariablePrompt({
  title,
  variables,
  onSubmit,
  onCancel,
}: {
  title: string;
  variables: readonly SnippetVariable[];
  onSubmit: (values: SnippetVariableValues) => void;
  onCancel: () => void;
}) {
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(variables.map((variable) => [variable.name, variable.defaultValue ?? ""])),
  );
  const [error, setError] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const formId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => modalRef.current?.querySelector<HTMLElement>("input, textarea") ?? null,
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const missing = variables.find((variable) => variable.required && values[variable.name]?.length === 0);
    if (missing) {
      setError(`必填变量「${missing.name}」不能为空`);
      return;
    }
    onSubmit(values);
  };

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      onCancel();
      return;
    }
    trapOverlayTab(event, modalRef.current);
  };

  return createPortal(
    <div
      className="nx-overlay"
      onClick={(event) => {
        event.stopPropagation();
        onCancel();
      }}
    >
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[480px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">{title}</span>
        </div>
        <form className="flex min-h-0 flex-col" noValidate onSubmit={submit}>
          <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
            <p className="nx-hint mb-3">变量值会原样替换到命令中；内建资产变量自动使用当前目标值。</p>
            {variables.map((variable, index) => {
              const inputId = `${formId}-${index}`;
              const value = values[variable.name] ?? "";
              return (
                <div className="nx-form-row" key={variable.name}>
                  <label className="nx-label" htmlFor={inputId}>
                    {variable.name}
                    <span className="ml-1.5 font-normal text-neutral-500">
                      {variable.required ? "必填" : "可选"}{variable.multiline ? " · 多行" : ""}
                    </span>
                  </label>
                  {variable.multiline ? (
                    <textarea
                      id={inputId}
                      className="nx-textarea font-mono text-[11.5px]"
                      rows={4}
                      value={value}
                      required={variable.required}
                      aria-label={`变量 ${variable.name}`}
                      onChange={(event) => {
                        setValues((current) => ({ ...current, [variable.name]: event.target.value }));
                        setError(null);
                      }}
                    />
                  ) : (
                    <input
                      id={inputId}
                      className="nx-input font-mono text-xs"
                      value={value}
                      required={variable.required}
                      aria-label={`变量 ${variable.name}`}
                      onChange={(event) => {
                        setValues((current) => ({ ...current, [variable.name]: event.target.value }));
                        setError(null);
                      }}
                    />
                  )}
                </div>
              );
            })}
            {error && (
              <div role="alert" className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
                <IconXCircle size={13} /> {error}
              </div>
            )}
          </div>
          <div className="nx-modal-footer shrink-0">
            <button type="button" className="nx-btn nx-btn-ghost" onClick={onCancel}>取消</button>
            <button type="submit" className="nx-btn nx-btn-primary">继续</button>
          </div>
        </form>
      </div>
    </div>,
    document.body,
  );
}

type PromptRequest = {
  id: number;
  title: string;
  variables: readonly SnippetVariable[];
  resolve: (values: SnippetVariableValues | null) => void;
};

export function useSnippetVariablePrompt(): {
  promptForValues: (title: string, variables: readonly SnippetVariable[]) => Promise<SnippetVariableValues | null>;
  promptElement: ReactNode;
  prompting: boolean;
} {
  const [request, setRequest] = useState<PromptRequest | null>(null);
  const nextId = useRef(0);
  const promptForValues = useCallback((title: string, variables: readonly SnippetVariable[]) => {
    if (variables.length === 0) return Promise.resolve<SnippetVariableValues>({});
    return new Promise<SnippetVariableValues | null>((resolve) => {
      nextId.current += 1;
      setRequest({ id: nextId.current, title, variables, resolve });
    });
  }, []);

  const finish = (values: SnippetVariableValues | null) => {
    if (!request) return;
    setRequest(null);
    request.resolve(values);
  };

  return {
    promptForValues,
    prompting: request !== null,
    promptElement: request ? (
      <SnippetVariablePrompt
        key={request.id}
        title={request.title}
        variables={request.variables}
        onSubmit={finish}
        onCancel={() => finish(null)}
      />
    ) : null,
  };
}
