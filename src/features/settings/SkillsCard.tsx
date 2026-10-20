import { useCallback, useEffect, useRef, useState } from "react";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconBot,
  IconEdit,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
import {
  isValidSkillName,
  type SkillEntry,
  type SkillsApi,
  type SkillOverrideInput,
} from "./skillsApi";

type Draft =
  | { kind: "create"; name: string; description: string; body: string }
  | { kind: "edit-loading"; entry: SkillEntry }
  | {
      kind: "edit";
      entry: SkillEntry;
      description: string;
      body: string;
      overrideDescription: boolean;
      overrideBody: boolean;
      disabled: boolean;
    };

function shortDigest(digest: string): string {
  return digest.length <= 19 ? digest : `${digest.slice(0, 19)}…`;
}

export function SkillsCard({ api }: { api: SkillsApi }) {
  const { pushToast } = useUi();

  const [entries, setEntries] = useState<SkillEntry[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [busyName, setBusyName] = useState<string | null>(null);

  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  const editGenRef = useRef(0);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    setError(null);
    try {
      const next = await api.list();
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setEntries(next);
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setEntries(null);
      setError(describeError(e));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, [api]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const closeDraft = () => {
    editGenRef.current++;
    setDraft(null);
    setFormError(null);
  };

  const openCreate = () => {
    setFormError(null);
    setDraft({ kind: "create", name: "", description: "", body: "" });
  };

  const openEdit = async (entry: SkillEntry) => {
    const gen = ++editGenRef.current;
    setFormError(null);
    setDraft({ kind: "edit-loading", entry });
    try {
      const material = await api.dispatch(entry.name);
      if (!aliveRef.current || gen !== editGenRef.current) return;
      setDraft({
        kind: "edit",
        entry,
        description: material.description,
        body: material.body,
        overrideDescription: entry.override?.description !== undefined,
        overrideBody: entry.override?.body !== undefined,
        disabled: entry.disabled,
      });
    } catch (e) {
      if (!aliveRef.current || gen !== editGenRef.current) return;
      setDraft(null);
      pushToast("error", `读取技能内容失败：${describeError(e)}`);
    }
  };

  const saveDraft = async () => {
    if (!draft || saving || draft.kind === "edit-loading") return;
    let input: SkillOverrideInput;
    if (draft.kind === "create") {
      const name = draft.name.trim();
      if (!isValidSkillName(name)) {
        setFormError("名称需为 64 字符以内的小写字母、数字与中划线组合");
        return;
      }
      if (!draft.description.trim() || !draft.body.trim()) {
        setFormError("描述和正文都不能为空");
        return;
      }
      input = { name, description: draft.description.trim(), body: draft.body };
    } else if (draft.entry.source === "custom") {
      if (!draft.description.trim() || !draft.body.trim()) {
        setFormError("描述和正文都不能为空");
        return;
      }
      input = {
        name: draft.entry.name,
        description: draft.description.trim(),
        body: draft.body,
        disabled: draft.disabled,
      };
    } else {
      if (!draft.overrideDescription && !draft.overrideBody && !draft.disabled) {
        setFormError("至少选择一项：覆盖描述、覆盖正文或禁用此技能");
        return;
      }
      input = { name: draft.entry.name, disabled: draft.disabled };
      if (draft.overrideDescription) input.description = draft.description.trim();
      if (draft.overrideBody) input.body = draft.body;
    }
    setSaving(true);
    setFormError(null);
    try {
      await api.saveOverride(input);
      pushToast("success", draft.kind === "create" ? "自定义技能已保存" : "技能覆盖已保存");
      setDraft(null);
      await reload();
    } catch (e) {
      setFormError(describeError(e));
    } finally {
      if (aliveRef.current) setSaving(false);
    }
  };

  const toggleDisable = async (entry: SkillEntry) => {
    if (busyName !== null) return;
    setBusyName(entry.name);
    try {
      if (entry.disabled) {
        const override = entry.override;
        if (override && (override.description !== undefined || override.body !== undefined)) {
          await api.saveOverride({
            name: entry.name,
            description: override.description,
            body: override.body,
            disabled: false,
          });
        } else {
          await api.deleteOverride(entry.name);
        }
        pushToast("success", `技能 ${entry.name} 已启用`);
      } else {
        await api.saveOverride({
          name: entry.name,
          description: entry.override?.description,
          body: entry.override?.body,
          disabled: true,
        });
        pushToast("success", `技能 ${entry.name} 已禁用`);
      }
      await reload();
    } catch (e) {
      pushToast("error", `${entry.disabled ? "启用" : "禁用"}失败：${describeError(e)}`);
    } finally {
      if (aliveRef.current) setBusyName(null);
    }
  };

  const remove = async (entry: SkillEntry) => {
    const custom = entry.source === "custom";
    const ok = await ask(
      custom
        ? `删除自定义技能 ${entry.name}？\n\n删除后 AI 将无法再加载该技能，且无法恢复。`
        : `删除技能 ${entry.name} 的覆盖？\n\n删除后恢复为内置内容，AI 将按内置版本加载该技能。`,
      { title: custom ? "删除自定义技能" : "删除技能覆盖", kind: "warning" },
    );
    if (!ok) return;
    setBusyName(entry.name);
    try {
      await api.deleteOverride(entry.name);
      pushToast("success", custom ? `自定义技能 ${entry.name} 已删除` : `技能 ${entry.name} 已恢复为内置版本`);
      await reload();
    } catch (e) {
      pushToast("error", `删除失败：${describeError(e)}`);
    } finally {
      if (aliveRef.current) setBusyName(null);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconBot size={15} className="text-purple-300" />
        <span className="nx-card-title">AI 技能</span>
        {entries && <span className="nx-badge">{entries.length} 个</span>}
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        技能是随应用内置或由你定义的 Markdown 指令文档，AI 按名称加载后注入模型。内置技能保持通用，不包含未经验证的具体命令；自定义技能请只写入你已验证的内容。每次下发都会固定内容的 SHA-256 摘要，便于核对实际注入的指令。
      </p>

      {draft === null ? (
        <div className="mb-3">
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={openCreate}>
            <IconPlus size={11} />
            新建自定义技能
          </button>
        </div>
      ) : draft.kind === "edit-loading" ? (
        <div
          className="mb-3 flex items-center gap-2 border-b border-neutral-800/60 pb-3"
          aria-busy="true"
        >
          <span className="text-[12.5px] text-neutral-200">读取技能内容…</span>
          <IconRefresh size={11} className="animate-spin text-neutral-500" />
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={closeDraft}>
            取消
          </button>
        </div>
      ) : (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="text-[12.5px] font-semibold text-neutral-200">
            {draft.kind === "create" ? "新建自定义技能" : `编辑技能 ${draft.entry.name}`}
          </div>
          {draft.kind === "create" && (
            <input
              className="nx-input nx-input-sm"
              placeholder="名称，小写字母、数字与中划线，例如 my-runbook"
              aria-label="技能名称"
              value={draft.name}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            />
          )}
          {draft.kind === "edit" && draft.entry.source === "embedded" && (
            <div className="flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
                <input
                  type="checkbox"
                  className="h-3.5 w-3.5"
                  aria-label="覆盖描述"
                  checked={draft.overrideDescription}
                  onChange={(e) => setDraft({ ...draft, overrideDescription: e.target.checked })}
                />
                覆盖描述
              </label>
              <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
                <input
                  type="checkbox"
                  className="h-3.5 w-3.5"
                  aria-label="覆盖正文"
                  checked={draft.overrideBody}
                  onChange={(e) => setDraft({ ...draft, overrideBody: e.target.checked })}
                />
                覆盖正文
              </label>
            </div>
          )}
          <input
            className="nx-input nx-input-sm"
            placeholder="描述，例如：整理巡检结果"
            aria-label="技能描述"
            disabled={draft.kind === "edit" && draft.entry.source === "embedded" && !draft.overrideDescription}
            value={draft.description}
            onChange={(e) => setDraft({ ...draft, description: e.target.value })}
          />
          <textarea
            className="nx-input min-h-[96px] font-mono text-[12px]"
            placeholder="正文，将作为模型指令注入"
            aria-label="技能正文"
            disabled={draft.kind === "edit" && draft.entry.source === "embedded" && !draft.overrideBody}
            value={draft.body}
            onChange={(e) => setDraft({ ...draft, body: e.target.value })}
          />
          {draft.kind === "edit" && (
            <div className="flex items-center gap-2 text-[11px] text-neutral-500">
              <span>当前摘要</span>
              <code className="font-mono" title={draft.entry.digest}>
                {shortDigest(draft.entry.digest)}
              </code>
            </div>
          )}
          {draft.kind === "edit" && (
            <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
              <input
                type="checkbox"
                className="h-3.5 w-3.5"
                aria-label="禁用此技能"
                checked={draft.disabled}
                onChange={(e) => setDraft({ ...draft, disabled: e.target.checked })}
              />
              禁用此技能
            </label>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={saving}
              onClick={() => void saveDraft()}
            >
              {saving ? "保存中…" : "保存"}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={saving}
              onClick={closeDraft}
            >
              取消
            </button>
          </div>
          {formError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2 text-[12px]" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{formError}</span>
            </div>
          )}
        </div>
      )}

      {entries === null ? (
        error ? (
          <div className="nx-alert nx-alert-danger flex items-start gap-2" role="alert">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">{error}</div>
              <button className="nx-btn nx-btn-outline nx-btn-sm mt-2" onClick={() => void reload()}>
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          </div>
        ) : (
          <div className="nx-hint py-2 text-[12px]" aria-busy="true">
            读取中…
          </div>
        )
      ) : entries.length === 0 ? (
        <div className="nx-hint py-2 text-[12px]">还没有技能。新建自定义技能后，AI 即可按名称加载。</div>
      ) : (
        <div className="flex flex-col gap-2">
          {entries.map((entry) => (
            <div key={entry.name} className="flex flex-col gap-1">
              <div className="flex flex-wrap items-center gap-2">
                <span
                  className="min-w-0 truncate font-mono text-[11.5px] text-neutral-300"
                  title={entry.name}
                >
                  {entry.name}
                </span>
                <span className="nx-badge shrink-0">{entry.source === "embedded" ? "内置" : "自定义"}</span>
                {entry.overridden && (
                  <span className="nx-badge nx-badge-amber shrink-0">已覆盖</span>
                )}
                {entry.disabled && <span className="nx-badge nx-badge-amber shrink-0">已禁用</span>}
                <div className="nx-spacer" />
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="编辑这个技能"
                  aria-label={`编辑技能 ${entry.name}`}
                  onClick={() => void openEdit(entry)}
                >
                  <IconEdit size={11} />
                </button>
                <button
                  className="nx-btn nx-btn-ghost nx-btn-sm"
                  aria-label={entry.disabled ? `启用技能 ${entry.name}` : `禁用技能 ${entry.name}`}
                  disabled={busyName !== null}
                  onClick={() => void toggleDisable(entry)}
                >
                  {entry.disabled ? "启用" : "禁用"}
                </button>
                {entry.override && (
                  <button
                    className="nx-icon-btn nx-icon-btn-sm is-danger"
                    title={entry.source === "custom" ? "删除这个技能" : "删除覆盖，恢复内置版本"}
                    aria-label={
                      entry.source === "custom"
                        ? `删除技能 ${entry.name}`
                        : `删除覆盖 ${entry.name}`
                    }
                    disabled={busyName !== null}
                    onClick={() => void remove(entry)}
                  >
                    {busyName === entry.name ? (
                      <IconRefresh size={11} className="animate-spin" />
                    ) : (
                      <IconTrash size={11} />
                    )}
                  </button>
                )}
              </div>
              <div className="break-words text-[12px] text-neutral-300">{entry.description}</div>
              <div className="flex flex-wrap items-center gap-2 text-[11px] text-neutral-500">
                <span>摘要</span>
                <code className="font-mono" title={entry.digest}>
                  {shortDigest(entry.digest)}
                </code>
                {entry.updatedAt > 0 && <span>更新于 {new Date(entry.updatedAt).toLocaleString()}</span>}
              </div>
            </div>
          ))}
        </div>
      )}

      <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
        技能覆盖保存在当前 NexTerm 实例。浏览器账号模式下，覆盖按账号隔离：只有当前账号能读写自己的覆盖，其他账号不可见；桌面和明确开放部署使用共享覆盖。删除覆盖会让内置技能恢复原始内容。
      </p>
    </section>
  );
}
