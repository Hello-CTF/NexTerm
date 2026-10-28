import { useEffect, useId, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import {
  KEYBINDING_ACTIONS,
  bindingConflicts,
  defaultBindingFor,
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
import type { SettingsPanelId } from "./settingsIndex";
import { isMac } from "../../app/platform";
import { WEB } from "../../ipc/env";
import { formatKeySequence } from "../terminal/terminalKeys";
import { terminalShortcutConflicts, type TerminalShortcutConflict } from "../terminal/terminalInput/shortcutConflicts";
import { ask } from "../../ui/dialogs";
import { IconChevronDown, IconChevronRight, IconClose, IconCommand, IconEdit, IconRefresh } from "../../ui/icons";

function describeWithheldInput(conflict: TerminalShortcutConflict): string {
  if (conflict.bytes === null) return conflict.meaning ?? "无（终端默认不发送）";
  const bytes = formatKeySequence(conflict.bytes);
  return conflict.meaning ? `${bytes}（${conflict.meaning}）` : bytes;
}

export function ShortcutsCard({ reveal }: { reveal?: { panel: SettingsPanelId; n: number } | null }) {
  const bindings = useKeybindings();
  const prefs = useInputPrefs();
  const [captureFor, setCaptureFor] = useState<KeybindingActionId | null>(null);
  const [captureError, setCaptureError] = useState<string | null>(null);
  const advancedPanelId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  useEffect(() => {
    if (reveal?.panel === "shortcuts-advanced") setAdvancedOpen(true);
  }, [reveal]);

  const conflicts = bindingConflicts(bindings);
  const terminalConflicts = terminalShortcutConflicts(bindings, isMac(), WEB);
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

  const resetAll = async () => {
    const ok = await ask("确定要恢复全部快捷键的默认设置吗？\n所有自定义快捷键将被重置。", { title: "全部恢复默认", kind: "warning" });
    if (ok) resetAllKeybindings();
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
        <div key={action.id} className="flex flex-col gap-1 text-xs" data-shortcut-row={action.id}>
          <input
            autoFocus
            readOnly
            className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
            placeholder={`按下新快捷键（当前 ${formatBinding(bindings[action.id])}）`}
            aria-label={`捕获快捷键：${action.label}`}
            aria-invalid={captureError ? true : undefined}
            onKeyDown={(event) => handleCaptureKey(action.id, event)}
            onBlur={finishCapture}
          />
          {captureError && (
            <p role="alert" className="text-xs text-red-300">
              {captureError}
            </p>
          )}
        </div>
      );
    }
    const peers = conflictPeers.get(action.id);
    return (
      <div
        key={action.id}
        className="flex items-center gap-2 text-xs text-neutral-400 @max-[479px]:flex-wrap @max-[479px]:gap-y-0.5 @max-[479px]:border-b @max-[479px]:border-neutral-800/50 @max-[479px]:pb-1.5"
        data-shortcut-row={action.id}
      >
        <span className="nx-kbd shrink-0">{formatBinding(bindings[action.id])}</span>
        <span className="min-w-0 flex-1 truncate @max-[479px]:order-first @max-[479px]:basis-full @max-[479px]:whitespace-normal @max-[479px]:break-words" title={action.label}>{action.label}</span>
        {peers && (
          <span className="nx-badge nx-badge-amber shrink-0" title={`与${peers}的快捷键冲突`}>
            冲突
          </span>
        )}
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm shrink-0 @max-[479px]:ml-auto"
          aria-label={`修改快捷键：${action.label}`}
          title="修改快捷键"
          onClick={() => startCapture(action.id)}
        >
          <IconEdit size={11} />
        </button>
        {bindings[action.id] !== null && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm shrink-0"
            aria-label={`清除快捷键：${action.label}`}
            title="清除快捷键"
            onClick={() => setKeybinding(action.id, null)}
          >
            <IconClose size={11} />
          </button>
        )}
        {bindings[action.id] !== defaultBindingFor(action) && (
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
    <section className="nx-card @container">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconCommand size={15} className="text-neutral-400" />
        <span className="nx-card-title">快捷键</span>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void resetAll()}>
          <IconRefresh size={11} />
          全部恢复默认
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        单击编辑图标后按下新的组合键；按 Esc 取消，按 Backspace 或 Delete 清除，也可单击 × 清除。浏览器保留的组合键（如 Ctrl+T）在网页版中无法拦截。
      </p>
      <div className="grid grid-cols-1 gap-x-6 gap-y-1.5 @min-[640px]:grid-cols-2">
        {KEYBINDING_ACTIONS.map(renderRow)}
      </div>
      {conflicts.length > 0 && (
        <p className="mt-2 text-[11px] text-amber-300">
          检测到 {conflicts.length} 组冲突：
          {conflicts
            .map(
              (conflict) =>
                `${conflict.actions[0].label}（${formatBinding(conflict.bindings[0])}）与 ${conflict.actions[1].label}（${formatBinding(conflict.bindings[1])}）重叠`,
            )
            .join("；")}
          。使用相同快捷键时，排在前面的操作生效。
        </p>
      )}
      {terminalConflicts.length > 0 && (
        <div className="mt-4 border-t border-neutral-800/60 pt-3.5" data-testid="terminal-shortcut-conflicts">
          <div className="text-[12.5px] text-neutral-200">与终端按键的冲突</div>
          <p className="nx-hint mt-0.5 mb-2">
            终端获得焦点时，下列组合键由应用处理，终端中运行的程序将无法接收；如需将按键保留给终端程序，请清除或修改对应的快捷键。
          </p>
          <div className="overflow-x-auto">
            <table className="nx-table text-[11.5px]" aria-label="与终端按键的冲突">
              <thead>
                <tr>
                  <th scope="col">按键</th>
                  <th scope="col">应用操作</th>
                  <th scope="col">终端程序默认接收</th>
                </tr>
              </thead>
              <tbody>
                {terminalConflicts.map((conflict) => (
                  <tr key={conflict.chord} data-terminal-conflict={conflict.action.id}>
                    <td className="whitespace-nowrap font-mono">{conflict.chord}</td>
                    <td>{conflict.action.label}</td>
                    <td>{describeWithheldInput(conflict)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
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
                <div className="text-[12.5px] text-neutral-200" data-setting="copy-on-select">选中自动复制</div>
                <p className="nx-hint mt-0.5">
                  在终端中选中文字后立即复制到剪贴板，并提示复制结果；默认关闭。
                  浏览器可能要求剪贴板权限，权限被拒绝时将显示提示。
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
        SQL 编辑器中的运行快捷键固定为 Ctrl+Enter，不在此列。
      </p>
    </section>
  );
}
