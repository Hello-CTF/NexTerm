import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MutableRefObject,
  type ReactNode,
  type RefObject,
} from "react";
import { createPortal, flushSync } from "react-dom";
import {
  TERMINAL_KEY_CATALOG,
  formatKeySequence,
  resetTerminalKeyConfig,
  resolveTerminalKeys,
  saveTerminalKeyConfig,
  terminalKeySequence,
  useTerminalKeyConfig,
  type TerminalKeySpec,
} from "./terminalKeys";
import { IconArrowDown, IconArrowUp, IconClose, IconSettings } from "../../ui/icons";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import {
  clearStickyModifiers,
  getStickyModifiers,
  toggleStickyModifier,
  useStickyModifiers,
} from "./terminalInput/stickyModifiers";
import { setTerminalInputMode, useTerminalInputMode } from "./terminalInputMode";
import { confirmQuickCommand, useQuickCommands, type QuickCommand } from "./quickCommands";
import { QuickCommandsEditor } from "./QuickCommandsEditor";

export interface TerminalKeysBarProps {
  onSend: (data: string) => void;
  onFocus: () => void;
  /**
   * Sends a line from buffered input or a quick command's text, followed by Enter when submit is set. The terminal
   * sends it as a paste (bracketed when enabled); without this prop the text and Enter are sent as plain bytes.
   */
  onSendText?: (text: string, submit: boolean) => void;
  /** Receives the buffered input field so a tap on the terminal can focus it. */
  composerRef?: MutableRefObject<HTMLInputElement | null>;
  broadcastCount?: number;
}

type ConfigFocusRequest = { id: string; control: "toggle" | "up" | "down" };

// Bar buttons must not take focus from the terminal or the buffered input, or the soft keyboard closes.
const keepFocus = (event: { preventDefault: () => void }) => event.preventDefault();

function BarDialog({
  label,
  onClose,
  initialFocus,
  footer,
  children,
}: {
  label: string;
  onClose: () => void;
  initialFocus?: RefObject<HTMLElement | null>;
  footer?: ReactNode;
  children: ReactNode;
}) {
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: initialFocus ? () => initialFocus.current : undefined,
  });

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onClose();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer, onClose]);

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      onClose();
      return;
    }
    if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
  };

  return createPortal(
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal nx-modal-sm"
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header">
          <span className="truncate">{label}</span>
          <div className="nx-spacer" />
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label={`关闭${label}`} onClick={onClose}>
            <IconClose size={12} />
          </button>
        </div>
        <div className="nx-modal-body">{children}</div>
        {footer && <div className="nx-modal-footer">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

export function TerminalKeysBar({ onSend, onFocus, onSendText, composerRef, broadcastCount }: TerminalKeysBarProps) {
  // Ctrl/Alt latch for the next key: a key on this bar or a character typed on the system keyboard.
  const { ctrl: ctrlActive, alt: altActive } = useStickyModifiers();
  const pushToast = useUi((s) => s.pushToast);
  const inputMode = useTerminalInputMode();
  const buffered = inputMode === "buffered";
  const quickCommands = useQuickCommands();
  const enabledIds = useTerminalKeyConfig();
  const [configOpen, setConfigOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState<"send" | "manage" | null>(null);
  const [draft, setDraft] = useState("");
  const [focusRequest, setFocusRequest] = useState<ConfigFocusRequest | null>(null);
  const configBodyRef = useRef<HTMLDivElement>(null);
  const doneButtonRef = useRef<HTMLButtonElement>(null);
  const composer = useRef<HTMLInputElement | null>(null);

  const keys = resolveTerminalKeys(enabledIds);
  const enabledSet = new Set(enabledIds);
  const disabledKeys = TERMINAL_KEY_CATALOG.filter((key) => !enabledSet.has(key.id));

  const clearModifiers = clearStickyModifiers;

  const sendText = (text: string, submit: boolean) => {
    if (onSendText) onSendText(text, submit);
    else onSend(`${text}${submit ? "\r" : ""}`);
  };

  const sendKey = (key: TerminalKeySpec) => {
    const modifiers = getStickyModifiers();
    clearStickyModifiers();
    onSend(terminalKeySequence(key, modifiers));
  };

  // A latch left behind by an unmounted bar must not alter later typing.
  useEffect(() => clearStickyModifiers, []);

  const saveLayout = (next: string[]) => {
    saveTerminalKeyConfig(next).catch((error: unknown) => {
      pushToast("error", `按键布局同步失败：${describeError(error)}`);
    });
  };

  const toggleKey = (id: string) => {
    setFocusRequest({ id, control: "toggle" });
    saveLayout(enabledIds.includes(id) ? enabledIds.filter((x) => x !== id) : [...enabledIds, id]);
  };

  const moveKey = (id: string, delta: -1 | 1) => {
    setFocusRequest({ id, control: delta === -1 ? "up" : "down" });
    const index = enabledIds.indexOf(id);
    const target = index + delta;
    if (index < 0 || target < 0 || target >= enabledIds.length) return;
    const next = [...enabledIds];
    [next[index], next[target]] = [next[target], next[index]];
    saveLayout(next);
  };

  const resetKeys = () => {
    resetTerminalKeyConfig().catch((error: unknown) => {
      pushToast("error", `按键布局同步失败：${describeError(error)}`);
    });
  };

  useEffect(() => {
    if (!focusRequest || !configOpen) return;
    const row = configBodyRef.current?.querySelector<HTMLElement>(`[data-key-row="${focusRequest.id}"]`);
    if (!row) {
      setFocusRequest(null);
      return;
    }
    let target: HTMLElement | null = null;
    if (focusRequest.control === "toggle") {
      target = row.querySelector<HTMLElement>("input[type=checkbox]");
    } else {
      const move = row.querySelectorAll<HTMLButtonElement>("button")[
        focusRequest.control === "up" ? 0 : 1
      ];
      target =
        move && !move.disabled
          ? move
          : row.querySelector<HTMLElement>("input[type=checkbox]");
    }
    target?.focus({ preventScroll: true });
    setFocusRequest(null);
  }, [focusRequest, configOpen, enabledIds]);

  // Focus moves synchronously inside the tap, which is what lets iOS and Android open the soft keyboard.
  const focusComposer = () => composer.current?.focus({ preventScroll: true });

  const toggleInputMode = () => {
    clearModifiers();
    if (buffered) {
      setTerminalInputMode("direct");
      onFocus();
      return;
    }
    flushSync(() => setTerminalInputMode("buffered"));
    focusComposer();
  };

  const submitLine = (submit: boolean) => {
    if (!draft && !submit) return;
    setDraft("");
    sendText(draft, submit);
  };

  const runQuickCommand = async (command: QuickCommand) => {
    setQuickOpen(null);
    if (!(await confirmQuickCommand(command))) return;
    sendText(command.text, command.appendEnter);
  };

  const renderConfigRow = (key: TerminalKeySpec, index: number | null) => (
    <div
      key={key.id}
      data-key-row={key.id}
      className="flex items-center gap-2 rounded-md border border-neutral-800/60 bg-neutral-900/50 px-2 py-1.5"
    >
      {index !== null && (
        <span className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            aria-label={`上移 ${key.title}`}
            disabled={index === 0}
            onClick={() => moveKey(key.id, -1)}
          >
            <IconArrowUp size={11} />
          </button>
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            aria-label={`下移 ${key.title}`}
            disabled={index === enabledIds.length - 1}
            onClick={() => moveKey(key.id, 1)}
          >
            <IconArrowDown size={11} />
          </button>
        </span>
      )}
      <span className="min-w-0 flex-1 truncate text-[12px] text-neutral-200">{key.title}</span>
      <span className="shrink-0 font-mono text-[10.5px] text-neutral-500">
        {formatKeySequence(key.data)}
      </span>
      <input
        type="checkbox"
        className="h-4 w-4 shrink-0"
        aria-label={`在按键条中显示「${key.title}」`}
        checked={enabledSet.has(key.id)}
        onChange={() => toggleKey(key.id)}
      />
    </div>
  );

  const configDialog = configOpen ? (
    <BarDialog
      label="终端按键配置"
      onClose={() => setConfigOpen(false)}
      initialFocus={doneButtonRef}
      footer={
        <>
          <button
            type="button"
            className="nx-btn nx-btn-ghost"
            onClick={resetKeys}
          >
            恢复默认
          </button>
          <button
            ref={doneButtonRef}
            type="button"
            className="nx-btn nx-btn-primary"
            onClick={() => setConfigOpen(false)}
          >
            完成
          </button>
        </>
      }
    >
      <div ref={configBodyRef}>
        <p className="nx-hint mb-3">
          勾选要在辅助按键条中显示的按键；已启用的按键可上下移动调整顺序。登录账号后，按键布局随账号同步到其他设备。
        </p>
        <div role="group" aria-label="已启用按键" className="mb-1 text-[11px] text-neutral-400">
          已启用
        </div>
        <div className="mb-3 flex max-h-[180px] flex-col gap-1 overflow-y-auto">
          {keys.map((key, index) => renderConfigRow(key, index))}
          {keys.length === 0 && (
            <div className="nx-hint px-1 py-2">没有已启用的按键，从下方勾选即可添加。</div>
          )}
        </div>
        <div role="group" aria-label="未启用按键" className="mb-1 text-[11px] text-neutral-400">
          未启用
        </div>
        <div className="flex max-h-[180px] flex-col gap-1 overflow-y-auto">
          {disabledKeys.map((key) => renderConfigRow(key, null))}
        </div>
      </div>
    </BarDialog>
  ) : null;

  const quickDialog = quickOpen ? (
    <BarDialog
      label="快捷命令"
      onClose={() => setQuickOpen(null)}
      footer={
        <button
          type="button"
          className="nx-btn nx-btn-ghost"
          onClick={() => setQuickOpen(quickOpen === "send" ? "manage" : "send")}
        >
          {quickOpen === "send" ? "管理快捷命令" : "返回发送"}
        </button>
      }
    >
      {quickOpen === "manage" ? (
        <QuickCommandsEditor />
      ) : quickCommands.length === 0 ? (
        <p className="nx-hint">还没有快捷命令。点击「管理快捷命令」添加，桌面和手机登录同一账号后共享。</p>
      ) : (
        <div className="flex flex-col gap-1" role="list" aria-label="发送快捷命令">
          {quickCommands.map((command, index) => (
            <button
              key={`${index}:${command.label}`}
              type="button"
              role="listitem"
              className="flex min-h-[40px] items-center gap-2 rounded-md border border-neutral-800/60 bg-neutral-900/50 px-2 py-1.5 text-left"
              aria-label={`发送快捷命令 ${command.label}`}
              onClick={() => void runQuickCommand(command)}
            >
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-[12.5px] text-neutral-200">{command.label}</span>
                <span className="truncate font-mono text-[11px] text-neutral-500">{command.text}</span>
              </span>
              {command.appendEnter && <span className="nx-badge shrink-0">回车</span>}
            </button>
          ))}
        </div>
      )}
    </BarDialog>
  ) : null;

  return (
    <>
      {buffered && (
        <div className="nx-terminal-composer" role="group" aria-label="缓冲输入">
          <input
            ref={(element) => {
              composer.current = element;
              if (composerRef) composerRef.current = element;
            }}
            className="nx-input min-w-0 flex-1"
            type="text"
            value={draft}
            enterKeyHint="send"
            autoCapitalize="none"
            aria-label="缓冲输入内容"
            placeholder="编辑整行后发送，支持听写"
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== "Enter" || isImeKeyEvent(event)) return;
              event.preventDefault();
              submitLine(true);
            }}
          />
          <button
            type="button"
            className="nx-terminal-key"
            title="发送这一行，不追加回车"
            aria-label="发送但不回车"
            onPointerDown={keepFocus}
            onClick={() => submitLine(false)}
          >
            不回车
          </button>
          <button
            type="button"
            className="nx-terminal-key is-active"
            title="发送这一行并回车"
            aria-label="发送并回车"
            onPointerDown={keepFocus}
            onClick={() => submitLine(true)}
          >
            发送
          </button>
        </div>
      )}
      <div className="nx-terminal-keys" aria-label="终端辅助按键">
        {broadcastCount != null && broadcastCount > 0 && (
          <span
            className="nx-badge nx-badge-red shrink-0"
            title="命令广播已开启：这里的按键将同时发送到多个终端"
          >
            广播 {broadcastCount}
          </span>
        )}
        <button
          type="button"
          className={`nx-terminal-key ${ctrlActive ? "is-active" : ""}`}
          title="下一个按键使用 Ctrl 修饰"
          aria-label="Ctrl 修饰键"
          aria-pressed={ctrlActive}
          onPointerDown={keepFocus}
          onClick={() => toggleStickyModifier("ctrl")}
        >
          Ctrl
        </button>
        <button
          type="button"
          className={`nx-terminal-key ${altActive ? "is-active" : ""}`}
          title="下一个按键使用 Alt 修饰"
          aria-label="Alt 修饰键"
          aria-pressed={altActive}
          onPointerDown={keepFocus}
          onClick={() => toggleStickyModifier("alt")}
        >
          Alt
        </button>
        {keys.map((key) => (
          <button
            type="button"
            key={key.id}
            className="nx-terminal-key"
            title={key.title}
            aria-label={key.title}
            onPointerDown={keepFocus}
            onClick={() => sendKey(key)}
          >
            {key.label}
          </button>
        ))}
        <button
          type="button"
          className="nx-terminal-key"
          title="快捷命令"
          aria-label="快捷命令"
          aria-haspopup="dialog"
          aria-expanded={quickOpen !== null}
          onPointerDown={keepFocus}
          onClick={(event) => {
            event.currentTarget.focus({ preventScroll: true });
            clearModifiers();
            setQuickOpen((open) => (open ? null : "send"));
          }}
        >
          命令
        </button>
        <button
          type="button"
          className="nx-terminal-key"
          title="配置终端按键"
          aria-label="配置终端按键"
          aria-haspopup="dialog"
          aria-expanded={configOpen}
          onPointerDown={keepFocus}
          onClick={(event) => {
            event.currentTarget.focus({ preventScroll: true });
            clearModifiers();
            setConfigOpen((open) => !open);
          }}
        >
          <IconSettings size={13} />
        </button>
        <div className="sticky right-0 ml-auto flex shrink-0 items-center gap-1 bg-[var(--nx-bg-canvas)] pl-1">
          <button
            type="button"
            className={`nx-terminal-key nx-terminal-keyboard ${buffered ? "is-active" : ""}`}
            title={buffered ? "缓冲输入已开启：点击改回直接输入" : "缓冲输入：先编辑整行再发送，适合听写和自动更正"}
            aria-label="缓冲输入"
            aria-pressed={buffered}
            onPointerDown={keepFocus}
            onClick={toggleInputMode}
          >
            整行
          </button>
          <button
            type="button"
            className="nx-terminal-key nx-terminal-keyboard"
            title={buffered ? "聚焦输入框并打开系统键盘" : "聚焦终端并打开系统键盘"}
            aria-label="聚焦终端并打开系统键盘"
            onPointerDown={keepFocus}
            onClick={() => {
              clearModifiers();
              if (buffered) focusComposer();
              else onFocus();
            }}
          >
            键盘
          </button>
        </div>
        {configDialog}
        {quickDialog}
      </div>
    </>
  );
}
