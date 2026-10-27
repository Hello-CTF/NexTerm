import { useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useUi } from "../../app/store";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert } from "../../ui/icons";
import {
  cancelMultilinePastePrompts,
  registerMultilinePasteHost,
  useMultilinePastePrompt,
  type MultilinePastePrompt,
} from "./multilinePastePrompt";
import { MULTILINE_PASTE_WARNING } from "./terminalPaste";

const CANCEL = { paste: false, skipTab: false, skipAlways: false } as const;

function MultilinePasteDialog({ prompt }: { prompt: MultilinePastePrompt }) {
  const modalRef = useRef<HTMLDivElement>(null);
  const pasteRef = useRef<HTMLButtonElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  // Cancel takes the initial focus, as the app's other warning dialogs do: a stray keystroke never pastes.
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => cancelRef.current });
  const titleId = useId();
  const descriptionId = useId();
  const [skipTab, setSkipTab] = useState(false);
  const [skipAlways, setSkipAlways] = useState(false);
  // An Enter still held from before the dialog opened must not confirm it: a confirming Enter needs a key
  // release seen while the dialog is open.
  const keyUpSeenRef = useRef(false);

  const confirm = () => prompt.settle({ paste: true, skipTab: prompt.tabScoped && skipTab, skipAlways });
  const cancel = () => prompt.settle(CANCEL);

  useEffect(() => {
    const onKeyUp = () => {
      keyUpSeenRef.current = true;
    };
    window.addEventListener("keyup", onKeyUp, true);
    return () => window.removeEventListener("keyup", onKeyUp, true);
  }, []);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      prompt.settle(CANCEL);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer, prompt]);

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    trapOverlayTab(event, modalRef.current);
    if (event.key !== "Enter" || isImeKeyEvent(event)) return;
    // An auto-repeated Enter never activates anything, including the focused button's native activation.
    if (event.repeat) {
      event.preventDefault();
      return;
    }
    // On the cancel button Enter keeps its native meaning; elsewhere in the dialog it pastes.
    if (event.target === cancelRef.current) return;
    event.preventDefault();
    if (!keyUpSeenRef.current || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    confirm();
  };

  const more = prompt.lineCount - prompt.preview.length;
  return (
    <div className="nx-overlay" onClick={cancel}>
      <div
        ref={modalRef}
        className="nx-modal nx-modal-sm"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className="nx-modal-header">
          <span className="flex shrink-0 text-amber-300" aria-hidden="true">
            <IconAlert size={14} />
          </span>
          <span id={titleId} className="truncate">粘贴多行文本</span>
        </div>
        <div className="nx-modal-body flex flex-col gap-3">
          <p id={descriptionId} className="text-[12.5px] leading-relaxed text-neutral-200">
            即将向终端粘贴 {prompt.lineCount} 行文本。{MULTILINE_PASTE_WARNING}
          </p>
          <pre
            aria-label="粘贴内容预览"
            className="max-h-40 overflow-auto rounded border border-neutral-800/70 bg-neutral-950/60 px-2.5 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre text-neutral-300"
          >
            {prompt.preview.join("\n")}
            {more > 0 && `\n…（共 ${prompt.lineCount} 行）`}
          </pre>
          <div className="flex flex-col gap-2 text-[12px] text-neutral-300">
            {prompt.tabScoped && (
              <label className="flex cursor-pointer items-center gap-2">
                <input
                  type="checkbox"
                  className="h-4 w-4 shrink-0"
                  checked={skipTab}
                  onChange={(event) => setSkipTab(event.target.checked)}
                />
                本终端不再提示
              </label>
            )}
            <label className="flex cursor-pointer items-start gap-2">
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                checked={skipAlways}
                onChange={(event) => setSkipAlways(event.target.checked)}
              />
              <span>
                以后都不再提示
                <span className="nx-hint block">可在 设置 &gt; 终端 &gt; 多行粘贴确认 中重新开启</span>
              </span>
            </label>
          </div>
        </div>
        <div className="nx-modal-footer">
          <button ref={cancelRef} type="button" className="nx-btn nx-btn-ghost" onClick={cancel}>
            取消
          </button>
          <button ref={pasteRef} type="button" className="nx-btn nx-btn-primary" onClick={confirm}>
            粘贴
          </button>
        </div>
      </div>
    </div>
  );
}

// MultilinePasteDialogHost shows the open multi-line paste prompts one at a time. An account switch cancels them,
// like the app's other dialogs, so no paste lands in a terminal of the previous account.
export function MultilinePasteDialogHost() {
  const prompt = useMultilinePastePrompt();
  const accountEpoch = useUi((state) => state.accountEpoch);
  const epochRef = useRef(accountEpoch);
  useEffect(() => registerMultilinePasteHost(), []);
  useEffect(() => {
    if (epochRef.current === accountEpoch) return;
    epochRef.current = accountEpoch;
    cancelMultilinePastePrompts();
  }, [accountEpoch]);
  if (!prompt) return null;
  return <MultilinePasteDialog key={prompt.id} prompt={prompt} />;
}
