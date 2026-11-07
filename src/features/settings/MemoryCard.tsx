import { useCallback, useEffect, useRef, useState } from "react";
import {
  memoryApi,
  type MemoryEntry,
  type MemoryIndexEntry,
  type MemorySecretPolicy,
  type MemorySettings,
  type MemoryTopicIndex,
} from "../../ipc/memory";
import { toAppError } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import {
  IconBot,
  IconEdit,
  IconKey,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
import { t as tr } from "../../i18n";

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

// The two stored switches are independent, so all four combinations are offered as one choice.
type MemoryMode = "off" | "inject" | "tools" | "both";

const MEMORY_MODES: { value: MemoryMode; label: string; description: string; injectionEnabled: boolean; toolsEnabled: boolean }[] = [
  { value: "off", label: tr("关闭"), description: tr("AI 不读取也不管理记忆"), injectionEnabled: false, toolsEnabled: false },
  { value: "inject", label: tr("仅引用记忆"), description: tr("把相关记忆加入模型输入，模型不能修改"), injectionEnabled: true, toolsEnabled: false },
  { value: "tools", label: tr("仅允许记忆工具"), description: tr("模型可用工具保存、查找和删除记忆"), injectionEnabled: false, toolsEnabled: true },
  { value: "both", label: tr("引用并允许管理"), description: tr("引用相关记忆，并允许模型保存、查找和删除"), injectionEnabled: true, toolsEnabled: true },
];

function memoryMode(settings: Pick<MemorySettings, "injectionEnabled" | "toolsEnabled">): MemoryMode {
  if (settings.injectionEnabled) return settings.toolsEnabled ? "both" : "inject";
  return settings.toolsEnabled ? "tools" : "off";
}

export function MemoryCard() {
  const pushToast = useUi((s) => s.pushToast);

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
    };
  }, []);

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    setError(null);
    try {
      const [nextSettings, nextTopics] = await Promise.all([
        memoryApi.settings(),
        memoryApi.index(),
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

  const chooseMode = async (mode: MemoryMode) => {
    if (!settings || settingsBusy) return;
    const next = MEMORY_MODES.find((option) => option.value === mode);
    if (!next || memoryMode(settings) === mode) return;
    setSettingsBusy(true);
    try {
      const updated = await memoryApi.setSettings(settings.version, {
        injectionEnabled: next.injectionEnabled,
        toolsEnabled: next.toolsEnabled,
      });
      if (aliveRef.current) setSettings(updated);
    } catch (e) {
      const conflict = versionConflict(e);
      if (conflict && aliveRef.current) {
        pushToast("error", tr("记忆设置已在其他位置被修改（版本 {expected} → {actual}），已重新读取", { expected: conflict.expected, actual: conflict.actual }));
        await reload();
      } else {
        pushToast("error", tr("保存记忆设置失败：{value}", { value: describeError(e) }));
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
      const entry = await memoryApi.get(id);
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
      const full = await memoryApi.get(entry.id);
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
      pushToast("error", tr("读取记忆正文失败：{value}", { value: describeError(e) }));
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
      setFormError(tr("主题和内容不能为空"));
      return;
    }
    setSaving(true);
    setFormError(null);
    try {
      if (draft.kind === "edit") {
        await memoryApi.edit(
          draft.editingId,
          draft.editingVersion,
          { topic, content, secrets: draft.secrets },
        );
        pushToast("success", tr("记忆已更新"));
      } else {
        await memoryApi.create(topic, content, draft.secrets);
        pushToast("success", tr("记忆已保存"));
      }
      setDraft(null);
      setExpanded({});
      await reload();
    } catch (e) {
      const conflict = versionConflict(e);
      if (conflict) {
        setFormError(
          tr("该记忆已在其他位置被修改（版本 {expected} → {actual}）。", { expected: conflict.expected, actual: conflict.actual }) +
            tr("列表已刷新，请基于最新内容重新编辑，以免覆盖他人的修改。"),
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
      tr("确定要删除这条记忆吗？\n主题：{topic}\n\n删除后，AI 将不再使用该记忆，且无法恢复。", { topic }),
      { title: tr("删除记忆"), kind: "warning" },
    );
    if (!ok) return;
    setDeletingId(entry.id);
    try {
      await memoryApi.delete(entry.id, entry.version);
      pushToast("success", tr("记忆已删除"));
      setExpanded((prev) => {
        const next = { ...prev };
        delete next[entry.id];
        return next;
      });
      await reload();
    } catch (e) {
      const conflict = versionConflict(e);
      if (conflict) {
        pushToast("error", tr("删除失败：该记忆已在其他位置被修改（版本 {expected} → {actual}），已重新读取", { expected: conflict.expected, actual: conflict.actual }));
        await reload();
      } else {
        pushToast("error", tr("删除失败：{value}", { value: describeError(e) }));
      }
    } finally {
      if (aliveRef.current) setDeletingId(null);
    }
  };

  const entryCount = topics?.reduce((n, t) => n + t.entries.length, 0) ?? 0;

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconBot size={15} className="text-purple-300" />
        <span className="nx-card-title">{tr("AI 长期记忆")}</span>
        {topics && <span className="nx-badge">{entryCount} {tr("条")}</span>}
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          {tr("刷新")}
        </button>
      </div>
      <p className="nx-hint mb-3.5">{tr("让 AI 跨会话记住长期事实，例如「重启安排在 02:00」。")}</p>

      <div className="mb-3 border-b border-neutral-800/60 pb-3">
        <div className="nx-setting-label mb-1.5" data-setting="memory-mode">{tr("AI 使用记忆")}</div>
        <Select
          ariaLabel={tr("AI 使用记忆")}
          className="w-full sm:w-auto"
          value={settings ? memoryMode(settings) : ""}
          placeholder={tr("正在加载…")}
          options={MEMORY_MODES.map(({ value, label, description }) => ({ value, label, description }))}
          disabled={!settings || settingsBusy}
          onChange={(value) => void chooseMode(value as MemoryMode)}
        />
        <p className="nx-hint mt-1.5">
          {settings ? MEMORY_MODES.find((mode) => mode.value === memoryMode(settings))?.description : ""}
          {settings?.toolsEnabled ? tr("；计划模式与子代理不可用记忆工具。") : ""}
        </p>
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
            {tr("新建记忆")}
          </button>
        </div>
      ) : draft.kind === "edit-loading" ? (
        <div
          className="mb-3 flex items-center gap-2 border-b border-neutral-800/60 pb-3"
          aria-busy="true"
        >
          <span className="text-[12.5px] text-neutral-200">{tr("正在读取记忆正文…")}</span>
          <IconRefresh size={11} className="animate-spin text-neutral-500" />
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={closeDraft}>
            {tr("取消")}
          </button>
        </div>
      ) : (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="text-[12.5px] font-semibold text-neutral-200">
            {draft.kind === "edit" ? tr("编辑记忆") : tr("新建记忆")}
          </div>
          <input
            className="nx-input nx-input-sm"
            placeholder={tr("主题，例如 operations")}
            aria-label={tr("记忆主题")}
            value={draft.topic}
            onChange={(e) => setDraft({ ...draft, topic: e.target.value })}
          />
          <textarea
            className="nx-input min-h-[72px] font-mono text-[12px]"
            placeholder={tr("内容，例如：生产环境重启安排在每周二 02:00")}
            aria-label={tr("记忆内容")}
            value={draft.content}
            onChange={(e) => setDraft({ ...draft, content: e.target.value })}
          />
          <div className="flex flex-wrap items-center gap-2">
            <IconKey size={12} className="text-neutral-500" />
            <Select
              ariaLabel={tr("密钥处理策略")}
              value={draft.secrets}
              options={[
                { value: "reject", label: tr("疑似密钥时拒绝写入（默认）") },
                { value: "redact", label: tr("疑似密钥时替换为 [REDACTED]") },
              ]}
              onChange={(value) => setDraft({ ...draft, secrets: value as MemorySecretPolicy })}
            />
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={saving}
              onClick={() => void saveDraft()}
            >
              {saving ? tr("正在保存…") : tr("保存")}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={saving}
              onClick={closeDraft}
            >
              {tr("取消")}
            </button>
          </div>
          <p className="nx-hint text-[11px]">
            {tr("请勿写入密码、令牌、API 密钥等敏感信息：默认策略将拒绝保存整条记忆；替换策略将把敏感内容替换为 [REDACTED] 后保存，并标记为「已脱敏」。")}
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
                {tr("重试")}
              </button>
            </div>
          </div>
        ) : (
          <div className="nx-hint py-2 text-[12px]" aria-busy="true">
            {tr("正在读取…")}
          </div>
        )
      ) : topics.length === 0 ? (
        <div className="nx-hint py-2 text-[12px]">{tr("暂无记忆。新建后，AI 运行时即可使用。")}</div>
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
                          title={open ? tr("收起正文") : tr("展开正文")}
                          onClick={() => void toggleExpand(entry.id)}
                        >
                          <span
                            className="min-w-0 truncate font-mono text-[11.5px] text-neutral-300"
                            title={entry.id}
                          >
                            {entry.id}
                          </span>
                          {entry.redacted && (
                            <span className="nx-badge nx-badge-amber shrink-0">{tr("已脱敏")}</span>
                          )}
                          <span className="nx-hint text-[11px]">
                            v{entry.version} · {new Date(entry.updatedAt).toLocaleString()}
                          </span>
                        </button>
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title={tr("编辑记忆")}
                          aria-label={tr("编辑记忆 {id}", { id: entry.id })}
                          onClick={() => void startEdit(entry)}
                        >
                          <IconEdit size={11} />
                        </button>
                        <button
                          className="nx-icon-btn nx-icon-btn-sm is-danger"
                          title={tr("删除记忆")}
                          aria-label={tr("删除记忆 {id}", { id: entry.id })}
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
                          {tr("正在读取正文…")}
                        </div>
                      )}
                      {open === "error" && (
                        <div className="nx-alert nx-alert-danger py-1 pl-1 text-[11px]" role="alert">
                          {tr("正文读取失败")}
                          <button
                            className="nx-btn nx-btn-ghost nx-btn-xs ml-2"
                            onClick={() => void toggleExpand(entry.id)}
                          >
                            {tr("重试")}
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
        {tr("记忆保存在当前 NexTerm 实例；浏览器账号模式按账号隔离，桌面版和开放部署共用同一份记忆。")}
      </p>
    </section>
  );
}
