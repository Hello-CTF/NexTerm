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
  | { kind: "edit"; entry: SkillEntry; description: string; body: string; disabled: boolean };

const BODY_PLACEHOLDER = "用 Markdown 写步骤和注意事项，例如：\n1. 先运行 nginx -t 检查配置\n2. 检查通过后再执行 systemctl reload nginx\n3. 失败时贴出报错，不要强行重启";

export function SkillsCard({ api }: { api: SkillsApi }) {
  const pushToast = useUi((s) => s.pushToast);

  // Entries are tagged with the api they were loaded from. A list loaded
  // for a previous owner never renders under the next owner's api, even
  // while the current owner's list is still in flight.
  const [entries, setEntries] = useState<{ api: SkillsApi; items: SkillEntry[] } | null>(null);
  const [loading, setLoading] = useState(true);
  // The list error is tagged the same way: a failure from a previous owner
  // never renders under the next owner's api, not even on the first
  // committed frame after a swap.
  const [errorState, setErrorState] = useState<{ api: SkillsApi; message: string } | null>(null);

  // Drafts are tagged the same way: an editor opened for a previous owner
  // is not part of the render under the next owner's api, even before
  // passive effects run.
  const [draftState, setDraftState] = useState<{ api: SkillsApi; value: Draft } | null>(null);
  const draft = draftState !== null && draftState.api === api ? draftState.value : null;
  const setDraft = useCallback(
    (next: Draft | null) => {
      setDraftState(next === null ? null : { api, value: next });
    },
    [api],
  );
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [busyName, setBusyName] = useState<string | null>(null);

  const visibleEntries = entries !== null && entries.api === api ? entries.items : null;
  const error = errorState !== null && errorState.api === api ? errorState.message : null;

  // Latest owner, assigned during render so owner checks see a swap in the
  // same commit — passive effects run too late for this.
  const apiRef = useRef(api);
  apiRef.current = api;

  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  const editGenRef = useRef(0);
  const epochRef = useRef(0);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  // The api prop identifies the owner scope. Every time it changes, the
  // previous owner's epoch ends: open drafts close, pending reads and
  // mutations are abandoned by the epoch checks below, and in-flight
  // list loads are superseded by the new owner's reload.
  useEffect(() => {
    epochRef.current += 1;
    editGenRef.current += 1;
    setDraft(null);
    setFormError(null);
    setSaving(false);
    setBusyName(null);
  }, [api, setDraft]);

  const reload = useCallback(async () => {
    // A refresh/retry callback retained from a previous owner must not
    // touch the current owner's load generation or backend: reject the
    // stale captured api before incrementing the shared generation or
    // issuing any call.
    if (apiRef.current !== api) return;
    const gen = ++loadGenRef.current;
    const epoch = epochRef.current;
    setLoading(true);
    setErrorState(null);
    try {
      const next = await api.list();
      if (!aliveRef.current || gen !== loadGenRef.current || epoch !== epochRef.current || apiRef.current !== api) return;
      setEntries({ api, items: next });
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current || epoch !== epochRef.current || apiRef.current !== api) return;
      setEntries(null);
      setErrorState({ api, message: describeError(e) });
    } finally {
      if (aliveRef.current && gen === loadGenRef.current && apiRef.current === api) setLoading(false);
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
    // Row handlers must compare their captured owner against the latest
    // api: a retained closure from before an owner swap still sees its own
    // render's entries/api, so only the apiRef check detects the swap.
    if (entries === null || entries.api !== api || apiRef.current !== api) return;
    const gen = ++editGenRef.current;
    const epoch = epochRef.current;
    setFormError(null);
    setDraft({ kind: "edit-loading", entry });
    try {
      const material = await api.getEffective(entry.name);
      if (!aliveRef.current || gen !== editGenRef.current || epoch !== epochRef.current || apiRef.current !== api) return;
      setDraft({ kind: "edit", entry, description: material.description, body: material.body, disabled: entry.disabled });
    } catch (e) {
      if (!aliveRef.current || gen !== editGenRef.current || epoch !== epochRef.current || apiRef.current !== api) return;
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
        setFormError("名称只能用小写字母、数字和中划线，最多 64 个字符，例如 nginx-check");
        return;
      }
      if (!draft.description.trim() || !draft.body.trim()) {
        setFormError("「什么时候用」和「内容」都要填写");
        return;
      }
      input = { name, description: draft.description.trim(), body: draft.body };
    } else {
      if (!draft.description.trim() || !draft.body.trim()) {
        setFormError("「什么时候用」和「内容」都要填写");
        return;
      }
      input = { name: draft.entry.name, description: draft.description.trim(), body: draft.body, disabled: draft.disabled };
    }
    // Re-check the draft's owner against the latest api immediately before
    // the backend call: a stale closure (e.g. a click delivered after a
    // synchronous owner swap) must never send the previous owner's draft
    // to the current owner's backend.
    if (draftState === null || draftState.api !== apiRef.current) return;
    setSaving(true);
    setFormError(null);
    const epoch = epochRef.current;
    try {
      await api.saveOverride(input);
      if (epoch !== epochRef.current) return;
      pushToast("success", draft.kind === "create" ? "技能已创建" : "技能已保存");
      setDraft(null);
      await reload();
    } catch (e) {
      if (epoch !== epochRef.current) return;
      setFormError(describeError(e));
    } finally {
      if (aliveRef.current && epoch === epochRef.current) setSaving(false);
    }
  };

  const toggleDisable = async (entry: SkillEntry) => {
    if (busyName !== null || entries === null || entries.api !== api || apiRef.current !== api) return;
    setBusyName(entry.name);
    const epoch = epochRef.current;
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
        if (epoch !== epochRef.current || apiRef.current !== api) return;
        pushToast("success", `已启用技能 ${entry.name}`);
      } else {
        await api.saveOverride({
          name: entry.name,
          description: entry.override?.description,
          body: entry.override?.body,
          disabled: true,
        });
        if (epoch !== epochRef.current || apiRef.current !== api) return;
        pushToast("success", `已停用技能 ${entry.name}，AI 不会再使用它`);
      }
      await reload();
    } catch (e) {
      if (epoch !== epochRef.current || apiRef.current !== api) return;
      pushToast("error", `${entry.disabled ? "启用" : "停用"}失败：${describeError(e)}`);
    } finally {
      if (aliveRef.current && epoch === epochRef.current && apiRef.current === api) setBusyName(null);
    }
  };

  const remove = async (entry: SkillEntry) => {
    if (entries === null || entries.api !== api || apiRef.current !== api) return;
    const epoch = epochRef.current;
    const builtin = entry.source === "embedded";
    const ok = builtin
      ? await ask(`恢复内置技能 ${entry.name} 的默认内容？\n\n你对它的修改和停用设置会被丢弃，技能恢复为内置版本并重新启用。内置技能本身不会被删除。`, { title: "恢复默认", kind: "warning" })
      : await ask(`删除技能 ${entry.name}？\n\n删除后 AI 不能再使用它，且无法恢复。`, { title: "删除技能", kind: "warning" });
    if (!ok || epoch !== epochRef.current || apiRef.current !== api) return;
    setBusyName(entry.name);
    try {
      await api.deleteOverride(entry.name);
      if (epoch !== epochRef.current || apiRef.current !== api) return;
      pushToast("success", builtin ? `已恢复技能 ${entry.name} 的默认内容` : `已删除技能 ${entry.name}`);
      await reload();
    } catch (e) {
      if (epoch !== epochRef.current || apiRef.current !== api) return;
      pushToast("error", `${builtin ? "恢复默认" : "删除"}失败：${describeError(e)}`);
    } finally {
      if (aliveRef.current && epoch === epochRef.current && apiRef.current === api) setBusyName(null);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconBot size={15} className="text-purple-300" />
        <span className="nx-card-title">AI 技能</span>
        {visibleEntries && <span className="nx-badge">{visibleEntries.length} 个</span>}
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        技能是你写给 AI 的操作手册。AI 会根据「什么时候用」自动判断要不要使用；在 AI 对话框输入 <code>/</code> 也可以直接选一个技能。
      </p>

      {draft === null ? (
        <div className="mb-3">
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={openCreate}>
            <IconPlus size={11} />
            新建技能
          </button>
        </div>
      ) : draft.kind === "edit-loading" ? (
        <div className="mb-3 flex items-center gap-2 border-b border-neutral-800/60 pb-3" aria-busy="true">
          <span className="text-[12.5px] text-neutral-200">读取技能内容…</span>
          <IconRefresh size={11} className="animate-spin text-neutral-500" />
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={closeDraft}>
            取消
          </button>
        </div>
      ) : (
        <div className="mb-3 flex flex-col gap-2.5 rounded-md border border-neutral-800/70 p-3">
          <div className="text-[12.5px] font-semibold text-neutral-200">
            {draft.kind === "create" ? "新建技能" : `编辑技能 ${draft.entry.name}`}
          </div>
          {draft.kind === "create" && (
            <label className="flex flex-col gap-1 text-[11.5px] text-neutral-400">
              名称
              <input
                className="nx-input nx-input-sm"
                placeholder="例如 nginx-check"
                aria-label="技能名称"
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              />
              <span className="nx-hint text-[11px]">小写字母、数字和中划线；在对话里输入 /{draft.name.trim() || "名称"} 就能直接使用</span>
            </label>
          )}
          <label className="flex flex-col gap-1 text-[11.5px] text-neutral-400">
            什么时候用
            <input
              className="nx-input nx-input-sm"
              placeholder="例如：检查 nginx 配置并安全地重载"
              aria-label="什么时候用"
              value={draft.description}
              onChange={(e) => setDraft({ ...draft, description: e.target.value })}
            />
            <span className="nx-hint text-[11px]">AI 靠这一句判断要不要使用这个技能，写清楚适用的场景</span>
          </label>
          <label className="flex flex-col gap-1 text-[11.5px] text-neutral-400">
            内容
            <textarea
              className="nx-input min-h-[200px] font-mono text-[12px] leading-relaxed"
              placeholder={BODY_PLACEHOLDER}
              aria-label="技能内容"
              value={draft.body}
              onChange={(e) => setDraft({ ...draft, body: e.target.value })}
            />
          </label>
          {formError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2 text-[12px]" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{formError}</span>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <div className="nx-spacer" />
            <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={saving} onClick={closeDraft}>
              取消
            </button>
            <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={saving} onClick={() => void saveDraft()}>
              {saving ? "保存中…" : "保存"}
            </button>
          </div>
        </div>
      )}

      {visibleEntries === null ? (
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
      ) : visibleEntries.length === 0 ? (
        draft === null && <div className="nx-hint py-2 text-[12px]">还没有技能。把你常用的排查或运维步骤写成技能，AI 遇到类似任务时会照着做。</div>
      ) : (
        <div className="flex flex-col gap-1.5">
          {visibleEntries.map((entry) => (
            <div key={entry.name} className="flex items-start gap-2.5 rounded-md border border-neutral-800/70 px-3 py-2">
              <input
                type="checkbox"
                className="mt-0.5 h-3.5 w-3.5 shrink-0"
                aria-label={entry.disabled ? `启用技能 ${entry.name}` : `停用技能 ${entry.name}`}
                title={entry.disabled ? "已停用，AI 不会使用" : "已启用"}
                checked={!entry.disabled}
                disabled={busyName !== null}
                onChange={() => void toggleDisable(entry)}
              />
              <div className="min-w-0 flex-1">
                <div
                  className={`truncate text-[12.5px] font-medium ${entry.disabled ? "text-neutral-500" : "text-neutral-200"}`}
                  title={`${entry.name} · ${entry.digest}`}
                >
                  {entry.name}
                </div>
                <div className={`break-words text-[12px] ${entry.disabled ? "text-neutral-500" : "text-neutral-400"}`}>{entry.description}</div>
              </div>
              <button
                className="nx-icon-btn nx-icon-btn-sm shrink-0"
                title="编辑这个技能"
                aria-label={`编辑技能 ${entry.name}`}
                onClick={() => void openEdit(entry)}
              >
                <IconEdit size={11} />
              </button>
              {entry.source === "embedded" ? (
                entry.override !== undefined && (
                  <button
                    className="nx-icon-btn nx-icon-btn-sm shrink-0"
                    title="恢复为内置默认内容"
                    aria-label={`恢复默认技能 ${entry.name}`}
                    disabled={busyName !== null}
                    onClick={() => void remove(entry)}
                  >
                    <IconRefresh size={11} className={busyName === entry.name ? "animate-spin" : undefined} />
                  </button>
                )
              ) : (
                <button
                  className="nx-icon-btn nx-icon-btn-sm is-danger shrink-0"
                  title="删除这个技能"
                  aria-label={`删除技能 ${entry.name}`}
                  disabled={busyName !== null}
                  onClick={() => void remove(entry)}
                >
                  {busyName === entry.name ? <IconRefresh size={11} className="animate-spin" /> : <IconTrash size={11} />}
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
