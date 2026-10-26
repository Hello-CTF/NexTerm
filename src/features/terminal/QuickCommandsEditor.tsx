import { useState } from "react";
import { IconArrowDown, IconArrowUp, IconEdit, IconPlus, IconTrash } from "../../ui/icons";
import { describeError } from "../../ui/errorText";
import {
  QUICK_COMMAND_LIMITS,
  quickCommandError,
  saveQuickCommands,
  useQuickCommands,
  type QuickCommand,
} from "./quickCommands";

type Draft = QuickCommand & { index: number | null };

const EMPTY_DRAFT: Draft = { index: null, label: "", text: "", appendEnter: true };

function previewText(text: string): string {
  return text.replace(/\r\n|\r|\n/g, " ⏎ ");
}

/** Adds, edits, reorders and deletes quick commands; used in settings and in the key bar's quick command panel. */
export function QuickCommandsEditor() {
  const commands = useQuickCommands();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const save = async (next: QuickCommand[]): Promise<boolean> => {
    setSaving(true);
    try {
      await saveQuickCommands(next);
      setError(null);
      return true;
    } catch (cause) {
      setError(`保存失败：${describeError(cause)}`);
      return false;
    } finally {
      setSaving(false);
    }
  };

  const move = (index: number, delta: -1 | 1) => {
    const target = index + delta;
    if (target < 0 || target >= commands.length) return;
    const next = [...commands];
    [next[index], next[target]] = [next[target], next[index]];
    void save(next);
  };

  const remove = (index: number) => {
    void save(commands.filter((_, i) => i !== index));
  };

  const submitDraft = async () => {
    if (!draft) return;
    const command: QuickCommand = { label: draft.label.trim(), text: draft.text, appendEnter: draft.appendEnter };
    const invalid = quickCommandError(command);
    if (invalid) {
      setError(invalid);
      return;
    }
    const next =
      draft.index === null
        ? [...commands, command]
        : commands.map((existing, i) => (i === draft.index ? command : existing));
    if (await save(next)) setDraft(null);
  };

  const full = commands.length >= QUICK_COMMAND_LIMITS.count;

  return (
    <div className="flex flex-col gap-2" aria-busy={saving}>
      {commands.length === 0 && !draft && (
        <p className="nx-hint">还没有快捷命令。添加常用命令后，可在终端按键条的「命令」中一键发送。</p>
      )}
      {commands.length > 0 && (
        <ul className="flex flex-col gap-1" aria-label="快捷命令列表">
          {commands.map((command, index) => (
            <li
              key={`${index}:${command.label}`}
              className="flex items-center gap-2 rounded-md border border-neutral-800/60 bg-neutral-900/50 px-2 py-1.5"
            >
              <span className="flex shrink-0 items-center gap-0.5">
                <button
                  type="button"
                  className="nx-icon-btn nx-icon-btn-sm"
                  aria-label={`上移 ${command.label}`}
                  disabled={index === 0 || saving}
                  onClick={() => move(index, -1)}
                >
                  <IconArrowUp size={11} />
                </button>
                <button
                  type="button"
                  className="nx-icon-btn nx-icon-btn-sm"
                  aria-label={`下移 ${command.label}`}
                  disabled={index === commands.length - 1 || saving}
                  onClick={() => move(index, 1)}
                >
                  <IconArrowDown size={11} />
                </button>
              </span>
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-[12.5px] text-neutral-200">{command.label}</span>
                <span className="truncate font-mono text-[11px] text-neutral-500">{previewText(command.text)}</span>
              </span>
              {command.appendEnter && <span className="nx-badge shrink-0">回车</span>}
              <button
                type="button"
                className="nx-icon-btn nx-icon-btn-sm shrink-0"
                aria-label={`编辑 ${command.label}`}
                disabled={saving}
                onClick={() => {
                  setError(null);
                  setDraft({ ...command, index });
                }}
              >
                <IconEdit size={12} />
              </button>
              <button
                type="button"
                className="nx-icon-btn nx-icon-btn-sm shrink-0"
                aria-label={`删除 ${command.label}`}
                disabled={saving}
                onClick={() => remove(index)}
              >
                <IconTrash size={12} />
              </button>
            </li>
          ))}
        </ul>
      )}
      {draft ? (
        <form
          className="flex flex-col gap-2 rounded-md border border-neutral-800/60 p-2"
          aria-label={draft.index === null ? "添加快捷命令" : "编辑快捷命令"}
          onSubmit={(event) => {
            event.preventDefault();
            void submitDraft();
          }}
        >
          <label className="flex flex-col gap-1">
            <span className="nx-setting-label">名称</span>
            <input
              className="nx-input"
              value={draft.label}
              maxLength={QUICK_COMMAND_LIMITS.labelChars * 2}
              placeholder="例如：查看状态"
              onChange={(event) => setDraft({ ...draft, label: event.target.value })}
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="nx-setting-label">发送内容</span>
            <textarea
              className="nx-textarea font-mono"
              rows={3}
              value={draft.text}
              spellCheck={false}
              autoCapitalize="off"
              autoCorrect="off"
              placeholder="例如：git status"
              onChange={(event) => setDraft({ ...draft, text: event.target.value })}
            />
          </label>
          <label className="flex cursor-pointer items-center gap-2 text-[12.5px] text-neutral-200">
            <input
              type="checkbox"
              className="h-4 w-4"
              checked={draft.appendEnter}
              onChange={(event) => setDraft({ ...draft, appendEnter: event.target.checked })}
            />
            发送后自动回车
          </label>
          <p className="nx-hint">内容包含多行时，发送前会先确认。</p>
          <div className="flex justify-end gap-2">
            <button
              type="button"
              className="nx-btn nx-btn-ghost"
              onClick={() => {
                setError(null);
                setDraft(null);
              }}
            >
              取消
            </button>
            <button type="submit" className="nx-btn nx-btn-primary" disabled={saving}>
              保存
            </button>
          </div>
        </form>
      ) : (
        <button
          type="button"
          className="nx-btn nx-btn-ghost self-start"
          disabled={full || saving}
          onClick={() => {
            setError(null);
            setDraft({ ...EMPTY_DRAFT });
          }}
        >
          <IconPlus size={12} />
          添加快捷命令
        </button>
      )}
      {full && !draft && <p className="nx-hint">最多保存 {QUICK_COMMAND_LIMITS.count} 条快捷命令。</p>}
      {error && (
        <p role="alert" className="text-[12px] text-red-400">
          {error}
        </p>
      )}
    </div>
  );
}
