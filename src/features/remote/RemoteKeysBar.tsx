import { useState } from "react";
import { MODIFIER_LABEL } from "./keys";
import { MODIFIER_KEYS, type ModifierKey } from "./remoteTypes";
import { t } from "../../i18n";

const keepFocus = (event: { preventDefault: () => void }) => event.preventDefault();

const BAR_KEYS: ReadonlyArray<{ code: string; label: string }> = [
  { code: "Escape", label: "Esc" },
  { code: "Tab", label: "Tab" },
  { code: "Enter", label: "Enter" },
  { code: "Backspace", label: "⌫" },
  { code: "ArrowLeft", label: "←" },
  { code: "ArrowUp", label: "↑" },
  { code: "ArrowDown", label: "↓" },
  { code: "ArrowRight", label: "→" },
  { code: "Home", label: "Home" },
  { code: "End", label: "End" },
  { code: "PageUp", label: "PgUp" },
  { code: "PageDown", label: "PgDn" },
];

const FUNCTION_KEYS = Array.from({ length: 12 }, (_, index) => `F${index + 1}`);

// The key bar of the remote pane on touch screens: the modifiers latch for the next key, then the usual navigation
// keys, and a keyboard button. It reuses the terminal key bar's look; what it sends is a remote key, not bytes.
export function RemoteKeysBar({
  latched,
  onToggleLatch,
  onKey,
  onKeyboard,
  keyboardActive,
}: {
  latched: ReadonlySet<ModifierKey>;
  onToggleLatch: (key: ModifierKey) => void;
  onKey: (code: string) => void;
  // Called inside the tap handler so the browser still counts it as a user gesture and opens the soft keyboard.
  onKeyboard: () => void;
  keyboardActive: boolean;
}) {
  const [functionOpen, setFunctionOpen] = useState(false);
  return (
    <div className="nx-terminal-keys" role="toolbar" aria-label={t("远程桌面辅助按键")} data-testid="remote-keys-bar">
      {MODIFIER_KEYS.map((key) => (
        <button
          key={key}
          type="button"
          className={`nx-terminal-key ${latched.has(key) ? "is-active" : ""}`}
          aria-pressed={latched.has(key)}
          onMouseDown={keepFocus}
          onClick={() => onToggleLatch(key)}
        >
          {MODIFIER_LABEL[key]}
        </button>
      ))}
      {BAR_KEYS.map((key) => (
        <button key={key.code} type="button" className="nx-terminal-key" onMouseDown={keepFocus} onClick={() => onKey(key.code)}>
          {key.label}
        </button>
      ))}
      <button
        type="button"
        className={`nx-terminal-key ${functionOpen ? "is-active" : ""}`}
        aria-expanded={functionOpen}
        onMouseDown={keepFocus}
        onClick={() => setFunctionOpen((open) => !open)}
      >
        F1-F12
      </button>
      {functionOpen &&
        FUNCTION_KEYS.map((code) => (
          <button key={code} type="button" className="nx-terminal-key" onMouseDown={keepFocus} onClick={() => onKey(code)}>
            {code}
          </button>
        ))}
      <button
        type="button"
        className={`nx-terminal-key nx-terminal-keyboard ${keyboardActive ? "is-active" : ""}`}
        aria-label={t("键盘")}
        onMouseDown={keepFocus}
        onClick={onKeyboard}
      >
        {t("键盘")}
      </button>
    </div>
  );
}
