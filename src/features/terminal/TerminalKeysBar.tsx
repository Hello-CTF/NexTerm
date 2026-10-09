import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createPortal } from "react-dom";
import {
  TERMINAL_KEY_CATALOG,
  formatKeySequence,
  loadTerminalKeyConfig,
  resetTerminalKeyConfig,
  resolveTerminalKeys,
  saveTerminalKeyConfig,
  terminalKeySequence,
  type TerminalKeySpec,
} from "./terminalKeys";
import { IconArrowDown, IconArrowUp, IconClose, IconSettings } from "../../ui/icons";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";

export interface TerminalKeysBarProps {
  onSend: (data: string) => void;
  onFocus: () => void;
  broadcastCount?: number;
}

type ConfigFocusRequest = { id: string; control: "toggle" | "up" | "down" };

export function TerminalKeysBar({ onSend, onFocus, broadcastCount }: TerminalKeysBarProps) {
  const [ctrlActive, setCtrlActive] = useState(false);
  const [altActive, setAltActive] = useState(false);
  const [configOpen, setConfigOpen] = useState(false);
  const [enabledIds, setEnabledIds] = useState<string[]>(loadTerminalKeyConfig);
  const [focusRequest, setFocusRequest] = useState<ConfigFocusRequest | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const doneButtonRef = useRef<HTMLButtonElement>(null);
  const layer = useOverlayFocus(configOpen, modalRef, {
    initialFocus: () => doneButtonRef.current,
  });

  const keys = resolveTerminalKeys(enabledIds);
  const enabledSet = new Set(enabledIds);
  const disabledKeys = TERMINAL_KEY_CATALOG.filter((key) => !enabledSet.has(key.id));

  const clearModifiers = () => {
    setCtrlActive(false);
    setAltActive(false);
  };

  const sendKey = (key: TerminalKeySpec) => {
    onSend(terminalKeySequence(key, { ctrl: ctrlActive, alt: altActive }));
    clearModifiers();
  };

  const toggleKey = (id: string) => {
    setFocusRequest({ id, control: "toggle" });
    setEnabledIds((current) => {
      const next = current.includes(id) ? current.filter((x) => x !== id) : [...current, id];
      saveTerminalKeyConfig(next);
      return next;
    });
  };

  const moveKey = (id: string, delta: -1 | 1) => {
    setFocusRequest({ id, control: delta === -1 ? "up" : "down" });
    setEnabledIds((current) => {
      const index = current.indexOf(id);
      const target = index + delta;
      if (index < 0 || target < 0 || target >= current.length) return current;
      const next = [...current];
      [next[index], next[target]] = [next[target], next[index]];
      saveTerminalKeyConfig(next);
      return next;
    });
  };

  const resetKeys = () => {
    setEnabledIds(resetTerminalKeyConfig());
  };

  useEffect(() => {
    if (!focusRequest || !configOpen) return;
    const row = modalRef.current?.querySelector<HTMLElement>(`[data-key-row="${focusRequest.id}"]`);
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

  useEffect(() => {
    if (!configOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      setConfigOpen(false);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [configOpen, layer]);

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      setConfigOpen(false);
      return;
    }
    if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
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
    <div className="nx-overlay" onClick={() => setConfigOpen(false)}>
      <div
        ref={modalRef}
        className="nx-modal max-w-[400px]"
        role="dialog"
        aria-modal="true"
        aria-label="终端按键配置"
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header">
          <span className="truncate">终端按键配置</span>
          <div className="nx-spacer" />
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            aria-label="关闭按键配置"
            onClick={() => setConfigOpen(false)}
          >
            <IconClose size={12} />
          </button>
        </div>
        <div className="nx-modal-body">
          <p className="nx-hint mb-3">
            勾选要在终端上方显示的按键；已启用的按键可上下移动调整顺序。设置只保存在本机，不同步到服务器。
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
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost" onClick={resetKeys}>
            重置默认
          </button>
          <button
            ref={doneButtonRef}
            type="button"
            className="nx-btn nx-btn-primary"
            onClick={() => setConfigOpen(false)}
          >
            完成
          </button>
        </div>
      </div>
    </div>
  ) : null;

  return (
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
        onPointerDown={(event) => event.preventDefault()}
        onClick={() => setCtrlActive((active) => !active)}
      >
        Ctrl
      </button>
      <button
        type="button"
        className={`nx-terminal-key ${altActive ? "is-active" : ""}`}
        title="下一个按键使用 Alt 修饰"
        aria-label="Alt 修饰键"
        aria-pressed={altActive}
        onPointerDown={(event) => event.preventDefault()}
        onClick={() => setAltActive((active) => !active)}
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
          onPointerDown={(event) => event.preventDefault()}
          onClick={() => sendKey(key)}
        >
          {key.label}
        </button>
      ))}
      <div className="sticky right-0 ml-auto flex shrink-0 items-center gap-1 bg-[var(--nx-bg-canvas)] pl-1">
        <button
          type="button"
          className="nx-terminal-key"
          title="配置终端按键"
          aria-label="配置终端按键"
          aria-haspopup="dialog"
          aria-expanded={configOpen}
          onPointerDown={(event) => event.preventDefault()}
          onClick={(event) => {
            event.currentTarget.focus({ preventScroll: true });
            clearModifiers();
            setConfigOpen((open) => !open);
          }}
        >
          <IconSettings size={13} />
        </button>
        <button
          type="button"
          className="nx-terminal-key nx-terminal-keyboard"
          title="聚焦终端并打开系统键盘"
          aria-label="聚焦终端并打开系统键盘"
          onPointerDown={(event) => event.preventDefault()}
          onClick={() => {
            clearModifiers();
            onFocus();
          }}
        >
          键盘
        </button>
      </div>
      {configDialog ? createPortal(configDialog, document.body) : null}
    </div>
  );
}
