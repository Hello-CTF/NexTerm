import { useCallback, useEffect, useRef, useState } from "react";
import {
  memoryApi,
  type MemoryEntry,
  type MemoryIndexEntry,
  type MemoryScope,
  type MemorySecretPolicy,
  type MemorySettings,
  type MemoryTopicIndex,
} from "../../ipc/memory";
import { toAppError } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconBot,
  IconEdit,
  IconKey,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

const MEMORY_SCOPE: MemoryScope = { tenant: "local", subject: "default" };

function versionConflict(e: unknown): { expected: number; actual: number } | null {
  const err = toAppError(e);
  if (err.code !== "bad_param" || !err.detail) return null;
  const { expected, actual } = err.detail;
  if (typeof expected !== "number" || typeof actual !== "number") return null;
  return { expected, actual };
}

type EntryDraft =
  | { kind: "create"; topic: string; content: string; secrets: MemorySecretPolicy }
  | { kind: "edit-loading"; editingId: string }
  | {
      kind: "edit";
      editingId: string;
      editingVersion: number;
      topic: string;
      content: string;
      secrets: MemorySecretPolicy;
    };

const CREATE_DRAFT = (): EntryDraft => ({
  kind: "create",
  topic: "",
  content: "",
  secrets: "reject",
});

export function MemoryCard() {
  const { pushToast } = useUi();

  const [settings, setSettings] = useState<MemorySettings | null>(null);
  const [topics, setTopics] = useState<MemoryTopicIndex[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [settingsBusy, setSettingsBusy] = useState(false);

  const [draft, setDraft] = useState<EntryDraft | null>(null);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const [expanded, setExpanded] = useState<Record<string, MemoryEntry | "loading" | "error">>({});

  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  const editGenRef = useRef(0);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      loadGenRef.current++;
      editGenRef.current++;
    };
  }, []);

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    setError(null);
    try {
      const [nextSettings, nextTopics] = await Promise.all([
        memoryApi.settings(MEMORY_SCOPE),
        memoryApi.index(MEMORY_SCOPE),
      ]);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setSettings(nextSettings);
      setTopics(nextTopics);
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setTopics(null);
      setSettings(null);
      setError(describeError(e));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  const toggleSetting = async (key: "injectionEnabled" | "toolsEnabled", next: boolean) => {
    if (!settings || settingsBusy) return;
    setSettingsBusy(true);
    try {
      const updated = await memoryApi.setSettings(MEMORY_SCOPE, settings.version, {
        [key]: next,
      });
      if (aliveRef.current) setSettings(updated);
    } catch (e) {
      const conflict = versionConflict(e);
      if (conflict && aliveRef.current) {
        pushToast("error", `开关已被其他地方修改（版本 ${conflict.expected} → ${conflict.actual}），已重新读取`);
        await reload();
      } else {
        pushToast("error", `保存开关失败：${describeError(e)}`);
        if (aliveRef.current) await reload();
      }
    } finally {
      if (aliveRef.current) setSettingsBusy(false);
    }
  };

  const toggleExpand = async (id: string) => {
    const current = expanded[id];
    if (current && current !== "error") {
      setExpanded((prev) => {
        const next = { ...prev };
        delete next[id];
        return next;
      });
      return;
    }
    setExpanded((prev) => ({ ...prev, [id]: "loading" }));
    try {
      const entry = await memoryApi.get(MEMORY_SCOPE, id);
      if (aliveRef.current) setExpanded((prev) => ({ ...prev, [id]: entry }));
    } catch {
      if (aliveRef.current) setExpanded((prev) => ({ ...prev, [id]: "error" }));
    }
  };

  const startEdit = async (entry: MemoryIndexEntry) => {
    const gen = ++editGenRef.current;
    setFormError(null);
    setDraft({ kind: "edit-loading", editingId: entry.id });
    try {
      const full = await memoryApi.get(MEMORY_SCOPE, entry.id);
      if (!aliveRef.current || gen !== editGenRef.current) return;
      setDraft({
        kind: "edit",
        editingId: full.id,
        editingVersion: full.version,
        topic: full.topic,
        content: full.content,
        secrets: "reject",
      });
    } catch (e) {
      if (!aliveRef.current || gen !== editGenRef.current) return;
      setDraft(null);
      pushToast("error", `读取记忆正文失败：${describeError(e)}`);
    }
  };

  const closeDraft = () => {
    editGenRef.current++;
    setDraft(null);
    setFormError(null);
  };

  const saveDraft = async () => {
    if (!draft || saving) return;
    if (draft.kind === "edit-loading") return;
    const topic = draft.topic.trim();
    const content = draft.content.trim();
    if (!topic || !content) {
      setFormError("主题和内容都不能为空");
      return;
    }
    setSaving(true);
    setFormError(null);
    try {
      if (draft.kind === "edit") {
        await memoryApi.edit(
          MEMORY_SCOPE,
          draft.editingId,
          draft.editingVersion,
          { topic, content, secrets: draft.secrets },
        );
        pushToast("success", "记忆已更新");
      } else {
        await memoryApi.create(MEMORY_SCOPE, topic, content, draft.secrets);
        pushToast("success", "记忆已保存");
      }
      setDraft(null);
      setExpanded({});
      await reload();
    } catch (e) {
      const conflict = versionConflict(e);
      if (conflict) {
        setFormError(
          `这条记忆已被其他地方修改（版本 ${conflict.expected} → ${conflict.actual}）。` +
            "已重新读取列表；请基于最新内容再改，避免把别人的修改盖掉。",
        );
        await reload();
      } else {
        setFormError(describeError(e));
      }
    } finally {
      if (aliveRef.current) setSaving(false);
    }
  };

  const remove = async (entry: MemoryIndexEntry, topic: string) => {
    const ok = await ask(
      `删除这条记忆？\n主题：${topic}\n\n删除后 AI 将不再记得它，且不可恢复。`,
      { title: "删除记忆", kind: "warning" },
    );
    if (!ok) return;
    setDeletingId(entry.id);
    try {
      await memoryApi.delete(MEMORY_SCOPE, entry.id, entry.version);
      pushToast("success", "记忆已删除");
      setExpanded((prev) => {
        const next = { ...prev };
        delete next[entry.id];
        return next;
      });
      await reload();
    } catch (e) {
      const conflict = versionConflict(e);
      if (conflict) {
        pushToast("error", `删除失败：这条记忆已被其他地方修改（版本 ${conflict.expected} → ${conflict.actual}），已重新读取`);
        await reload();
      } else {
        pushToast("error", `删除失败：${describeError(e)}`);
      }
    } finally {
      if (aliveRef.current) setDeletingId(null);
    }
  };

  const entryCount = topics?.reduce((n, t) => n + t.entries.length, 0) ?? 0;

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconBot size={15} className="text-purple-300" />
        <span className="nx-card-title">AI 长期记忆</span>
        {topics && <span className="nx-badge">{entryCount} 条</span>}
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        让 AI 跨会话记住长期事实（如「重启安排在 02:00」）。默认关闭，逐项开启；
        记忆按 owner scope 隔离，只注入开启了的运行。
      </p>

      <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
        <label className="flex cursor-pointer items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="text-[12.5px] text-neutral-200">注入到 AI 运行</div>
            <p className="nx-hint mt-0.5">
              开启后每次 AI 运行会把选中的记忆作为一条临时系统消息注入模型输入；
              不写会话历史，下一轮重新注入。
            </p>
          </div>
          <input
            type="checkbox"
            className="mt-0.5 h-4 w-4 shrink-0"
            aria-label="注入到 AI 运行"
            disabled={!settings || settingsBusy}
            checked={settings?.injectionEnabled ?? false}
            onChange={(e) => void toggleSetting("injectionEnabled", e.target.checked)}
          />
        </label>
        <label className="flex cursor-pointer items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="text-[12.5px] text-neutral-200">允许模型使用记忆工具</div>
            <p className="nx-hint mt-0.5">
              开启后模型可以自己保存 / 查找 / 遗忘记忆（memory_save / memory_list /
              memory_recall / memory_forget）；计划模式与子代理不可用。
            </p>
          </div>
          <input
            type="checkbox"
            className="mt-0.5 h-4 w-4 shrink-0"
            aria-label="允许模型使用记忆工具"
            disabled={!settings || settingsBusy}
            checked={settings?.toolsEnabled ?? false}
            onChange={(e) => void toggleSetting("toolsEnabled", e.target.checked)}
          />
        </label>
      </div>

      {draft === null ? (
        <div className="mb-3">
          <button
            className="nx-btn nx-btn-outline nx-btn-sm"
            onClick={() => {
              setFormError(null);
              setDraft(CREATE_DRAFT());
            }}
          >
            <IconPlus size={11} />
            新建一条记忆
          </button>
        </div>
      ) : draft.kind === "edit-loading" ? (
        <div
          className="mb-3 flex items-center gap-2 border-b border-neutral-800/60 pb-3"
          aria-busy="true"
        >
          <span className="text-[12.5px] text-neutral-200">读取记忆正文…</span>
          <IconRefresh size={11} className="animate-spin text-neutral-500" />
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={closeDraft}>
            取消
          </button>
        </div>
      ) : (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="text-[12.5px] font-semibold text-neutral-200">
            {draft.kind === "edit" ? "编辑记忆" : "新建记忆"}
          </div>
          <input
            className="nx-input nx-input-sm"
            placeholder="主题，例如 operations"
            aria-label="记忆主题"
            value={draft.topic}
            onChange={(e) => setDraft({ ...draft, topic: e.target.value })}
          />
          <textarea
            className="nx-input min-h-[72px] font-mono text-[12px]"
            placeholder="内容，例如：生产环境重启安排在每周二 02:00"
            aria-label="记忆内容"
            value={draft.content}
            onChange={(e) => setDraft({ ...draft, content: e.target.value })}
          />
          <div className="flex flex-wrap items-center gap-2">
            <IconKey size={12} className="text-neutral-500" />
            <select
              className="nx-select nx-input-sm"
              aria-label="密钥处理策略"
              value={draft.secrets}
              onChange={(e) =>
                setDraft({ ...draft, secrets: e.target.value as MemorySecretPolicy })
              }
            >
              <option value="reject">疑似密钥时拒绝写入（默认）</option>
              <option value="redact">疑似密钥时替换为 [REDACTED]</option>
            </select>
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
          <p className="nx-hint text-[11px]">
            不要写入密码、token、api_key 等密钥：默认策略会整篇拒绝；选替换则值会变成
            [REDACTED] 后保存。已被脱敏的条目会带「已脱敏」标记。
          </p>
          {formError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2 text-[12px]" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{formError}</span>
            </div>
          )}
        </div>
      )}

      {topics === null ? (
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
      ) : topics.length === 0 ? (
        <div className="nx-hint py-2 text-[12px]">还没有记忆。开启开关后，AI 运行会注入这里的内容。</div>
      ) : (
        <div className="flex flex-col gap-2">
          {topics.map((t) => (
            <div key={t.topic}>
              <div className="mb-1 break-words font-mono text-[11px] text-neutral-500">{t.topic}</div>
              <div className="flex flex-col gap-1">
                {t.entries.map((entry) => {
                  const open = expanded[entry.id];
                  return (
                    <div key={entry.id} className="flex flex-col gap-1">
                      <div className="flex items-center gap-2">
                        <button
                          className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-0.5 text-left"
                          aria-expanded={!!open}
                          title={open ? "收起正文" : "展开正文"}
                          onClick={() => void toggleExpand(entry.id)}
                        >
                          <span
                            className="min-w-0 truncate font-mono text-[11.5px] text-neutral-300"
                            title={entry.id}
                          >
                            {entry.id}
                          </span>
                          {entry.redacted && (
                            <span className="nx-badge nx-badge-amber shrink-0">已脱敏</span>
                          )}
                          <span className="nx-hint text-[11px]">
                            v{entry.version} · {new Date(entry.updatedAt).toLocaleString()}
                          </span>
                        </button>
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title="编辑这条记忆"
                          aria-label={`编辑记忆 ${entry.id}`}
                          onClick={() => void startEdit(entry)}
                        >
                          <IconEdit size={11} />
                        </button>
                        <button
                          className="nx-icon-btn nx-icon-btn-sm is-danger"
                          title="删除这条记忆"
                          aria-label={`删除记忆 ${entry.id}`}
                          disabled={deletingId !== null}
                          onClick={() => void remove(entry, t.topic)}
                        >
                          {deletingId === entry.id ? (
                            <IconRefresh size={11} className="animate-spin" />
                          ) : (
                            <IconTrash size={11} />
                          )}
                        </button>
                      </div>
                      {open === "loading" && (
                        <div className="nx-hint py-1 pl-1 text-[11px]" aria-busy="true">
                          读取正文…
                        </div>
                      )}
                      {open === "error" && (
                        <div className="nx-alert nx-alert-danger py-1 pl-1 text-[11px]" role="alert">
                          正文读取失败
                          <button
                            className="nx-btn nx-btn-ghost nx-btn-xs ml-2"
                            onClick={() => void toggleExpand(entry.id)}
                          >
                            重试
                          </button>
                        </div>
                      )}
                      {open && open !== "loading" && open !== "error" && (
                        <pre className="whitespace-pre-wrap break-words rounded border border-neutral-800/60 bg-neutral-900/60 p-2 font-mono text-[11.5px] text-neutral-300">
                          {open.content}
                        </pre>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      )}

      <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
        记忆按 owner scope（{MEMORY_SCOPE.tenant} / {MEMORY_SCOPE.subject}）隔离存储，
        与 AI 运行使用的是同一份；其它 scope 读不到也改不到这里的内容。
      </p>
    </section>
  );
}
