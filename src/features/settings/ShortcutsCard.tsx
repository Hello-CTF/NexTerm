import { useId, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import {
  KEYBINDING_ACTIONS,
  bindingConflicts,
  captureBindingForAction,
  formatBinding,
  resetAllKeybindings,
  resetKeybinding,
  setKeybinding,
  useKeybindings,
  type CaptureResult,
  type KeybindingAction,
  type KeybindingActionId,
} from "../../app/keybindings";
import { setSelectionAutoCopy, useInputPrefs } from "../../app/preferences";
import { IconChevronDown, IconChevronRight, IconCommand, IconEdit, IconRefresh } from "../../ui/icons";

export function ShortcutsCard() {
  const bindings = useKeybindings();
  const prefs = useInputPrefs();
  const [captureFor, setCaptureFor] = useState<KeybindingActionId | null>(null);
  const [captureError, setCaptureError] = useState<string | null>(null);
  const advancedPanelId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(() => prefs.selectionAutoCopy);

  const conflicts = bindingConflicts(bindings);
  const conflictPeers = new Map<string, string>();
  for (const conflict of conflicts) {
    const [first, second] = conflict.actions;
    conflictPeers.set(
      first.id,
      [conflictPeers.get(first.id), `${second.label}（${formatBinding(conflict.bindings[1])}）`]
        .filter(Boolean)
        .join("、"),
    );
    conflictPeers.set(
      second.id,
      [conflictPeers.get(second.id), `${first.label}（${formatBinding(conflict.bindings[0])}）`]
        .filter(Boolean)
        .join("、"),
    );
  }

  const startCapture = (id: KeybindingActionId) => {
    setCaptureError(null);
    setCaptureFor(id);
  };

  const finishCapture = () => {
    setCaptureFor(null);
    setCaptureError(null);
  };

  const handleCaptureKey = (id: KeybindingActionId, event: ReactKeyboardEvent<HTMLInputElement>) => {
    event.preventDefault();
    event.stopPropagation();
    if (event.repeat) return;
    if (event.key === "Escape") {
      finishCapture();
      return;
    }
    if (event.key === "Backspace" || event.key === "Delete") {
      setKeybinding(id, null);
      finishCapture();
      return;
    }
    const result: CaptureResult = captureBindingForAction(event.nativeEvent, id);
    if (result.kind === "modifier") return;
    if (result.kind === "invalid") {
      setCaptureError(result.reason);
      return;
    }
    setKeybinding(id, result.binding);
    finishCapture();
  };

  const renderRow = (action: KeybindingAction) => {
    if (captureFor === action.id) {
      return (
        <div key={action.id} className="flex items-center gap-2 text-xs" data-shortcut-row={action.id}>
          <input
            autoFocus
            readOnly
            className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
            placeholder={`按下新快捷键（当前 ${formatBinding(bindings[action.id])}）`}
            aria-label={`捕获快捷键：${action.label}`}
            onKeyDown={(event) => handleCaptureKey(action.id, event)}
            onBlur={finishCapture}
          />
        </div>
      );
    }
    const peers = conflictPeers.get(action.id);
    return (
      <div
        key={action.id}
        className="flex items-center gap-2 text-xs text-neutral-400"
        data-shortcut-row={action.id}
      >
        <span className="nx-kbd shrink-0">{formatBinding(bindings[action.id])}</span>
        <span className="min-w-0 flex-1 truncate">{action.label}</span>
        {peers && (
          <span className="nx-badge nx-badge-amber shrink-0" title={`与${peers}的快捷键重叠`}>
            冲突
          </span>
        )}
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm shrink-0"
          aria-label={`修改快捷键：${action.label}`}
          title="修改快捷键"
          onClick={() => startCapture(action.id)}
        >
          <IconEdit size={11} />
        </button>
        {bindings[action.id] !== action.defaultBinding && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm shrink-0"
            aria-label={`恢复默认快捷键：${action.label}`}
            title="恢复默认"
            onClick={() => resetKeybinding(action.id)}
          >
            <IconRefresh size={11} />
          </button>
        )}
      </div>
    );
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconCommand size={15} className="text-neutral-400" />
        <span className="nx-card-title">快捷键</span>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={resetAllKeybindings}>
          <IconRefresh size={11} />
          全部恢复默认
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        点「编辑」后按下新的组合键完成改绑；捕获时 Esc 取消、Backspace
        清除绑定。浏览器保留的组合键（如 Ctrl+T 新开浏览器标签）在网页版里无法拦截。
      </p>
      <div className="grid grid-cols-1 gap-x-6 gap-y-1.5 min-[480px]:grid-cols-2">
        {KEYBINDING_ACTIONS.map(renderRow)}
      </div>
      {captureError && <p className="mt-2 text-[11px] text-red-300">{captureError}</p>}
      {conflicts.length > 0 && (
        <p className="mt-2 text-[11px] text-amber-300">
          检测到 {conflicts.length} 组冲突：
          {conflicts
            .map(
              (conflict) =>
                `${conflict.actions[0].label}（${formatBinding(conflict.bindings[0])}）与 ${conflict.actions[1].label}（${formatBinding(conflict.bindings[1])}）重叠`,
            )
            .join("；")}
          。同时按下时排在前面的动作生效。
        </p>
      )}
      <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
        <button
          type="button"
          className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
          aria-expanded={advancedOpen}
          aria-controls={advancedPanelId}
          onClick={() => setAdvancedOpen((v) => !v)}
        >
          {advancedOpen ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          高级设置
        </button>
        {advancedOpen && (
          <div id={advancedPanelId} className="mt-2">
            <label className="flex cursor-pointer items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="text-[12.5px] text-neutral-200">选中自动复制</div>
                <p className="nx-hint mt-0.5">
                  在终端里选中文字后立即复制到剪贴板，成功或失败都会弹出提示；默认关闭。
                  浏览器可能要求剪贴板权限，被拒绝时会如实提示。
                </p>
              </div>
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                aria-label="选中自动复制"
                checked={prefs.selectionAutoCopy}
                onChange={(event) => setSelectionAutoCopy(event.target.checked)}
              />
            </label>
          </div>
        )}
      </div>
      <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
        SQL 编辑器内运行固定为 Ctrl+Enter，不在此列。
      </p>
    </section>
  );
}
