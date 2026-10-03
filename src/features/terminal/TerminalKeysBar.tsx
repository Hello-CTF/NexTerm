import { useState } from "react";
import { TERMINAL_KEYS, terminalKeySequence } from "./terminalKeys";

export interface TerminalKeysBarProps {
  onSend: (data: string) => void;
  onFocus: () => void;
}

export function TerminalKeysBar({ onSend, onFocus }: TerminalKeysBarProps) {
  const [ctrlActive, setCtrlActive] = useState(false);

  return (
    <div className="nx-terminal-keys" aria-label="终端辅助按键">
      <button
        type="button"
        className={`nx-terminal-key ${ctrlActive ? "is-active" : ""}`}
        title="下一个按键使用 Ctrl 修饰"
        aria-label="Ctrl 修饰键"
        aria-pressed={ctrlActive}
        onPointerDown={(event) => event.preventDefault()}
        onClick={() => setCtrlActive((active) => !active)}
      >
        Ctrl
      </button>
      {TERMINAL_KEYS.map((key) => (
        <button
          type="button"
          key={key.id}
          className="nx-terminal-key"
          title={key.title}
          aria-label={key.title}
          onPointerDown={(event) => event.preventDefault()}
          onClick={() => {
            onSend(terminalKeySequence(key, ctrlActive));
            setCtrlActive(false);
          }}
        >
          {key.label}
        </button>
      ))}
      <button
        type="button"
        className="nx-terminal-key nx-terminal-keyboard"
        title="聚焦终端并打开系统键盘"
        aria-label="聚焦终端并打开系统键盘"
        onPointerDown={(event) => event.preventDefault()}
        onClick={() => {
          setCtrlActive(false);
          onFocus();
        }}
      >
        键盘
      </button>
    </div>
  );
}
