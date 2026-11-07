import { useEffect, useMemo, useRef, useState } from "react";
import { aiApi, type AiPermissionConfig } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import { IconClose, IconPlus, IconShield } from "../../ui/icons";
import { showSettingsSection } from "./settingsNavigation";
import { t } from "../../i18n";

const RULE_PAGE_SIZE = 8;

// AiRulesCard edits the danger rules the AI must confirm or refuse. Each
// change saves immediately; writes are serialized and always apply to the
// latest saved list, so quick consecutive edits never overwrite each other.
export function AiRulesCard() {
  const pushToast = useUi((s) => s.pushToast);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const rulePrefill = useUi((s) => s.aiRulePrefill);
  const [aiPerm, setAiPerm] = useState<AiPermissionConfig | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState("");
  const [draftOpen, setDraftOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const pending = useRef(0);

  const load = () => {
    setLoadError(null);
    aiApi
      .getPermission()
      .then(setAiPerm)
      .catch((e: unknown) => setLoadError(describeError(e)));
  };
  useEffect(() => {
    setAiPerm(null);
    load();
  }, [accountEpoch]);

  useEffect(() => {
    if (rulePrefill === null) return;
    setDraft(rulePrefill);
    setDraftOpen(true);
    useUi.getState().setAiRulePrefill(null);
    showSettingsSection("settings-ai-rules");
  }, [rulePrefill]);

  const q = query.trim().toLowerCase();
  const rules = useMemo(() => aiPerm?.dangerRules ?? [], [aiPerm?.dangerRules]);
  const filtered = useMemo(() => rules.filter((r) => !q || r.toLowerCase().includes(q)), [rules, q]);
  const pageCount = Math.max(1, Math.ceil(filtered.length / RULE_PAGE_SIZE));
  const currentPage = Math.min(page, pageCount - 1);
  const pageRules = filtered.slice(currentPage * RULE_PAGE_SIZE, (currentPage + 1) * RULE_PAGE_SIZE);

  // change computes the new list from the latest saved one; it returns false
  // (and saves nothing) when transform declines the change.
  const change = (transform: (current: string[]) => string[] | null, done: string): Promise<boolean> => {
    pending.current += 1;
    setSaving(true);
    const run = queue.current.then(async () => {
      try {
        const latest = await aiApi.getPermission();
        const next = transform(latest.dangerRules);
        if (!next) return false;
        await aiApi.setPermission({ ...latest, dangerRules: next });
        setAiPerm({ ...latest, dangerRules: next });
        pushToast("success", done);
        return true;
      } catch (e) {
        pushToast("error", t("保存拦截规则失败：{value}。规则未改动，请重试。", { value: describeError(e) }));
        load();
        return false;
      }
    });
    queue.current = run;
    void run.then(() => {
      pending.current -= 1;
      if (pending.current === 0) setSaving(false);
    });
    return run;
  };

  const has = (list: string[], rule: string, except?: string) =>
    list.some((x) => x !== except && x.toLowerCase() === rule.toLowerCase());

  const addRule = async () => {
    const r = draft.trim();
    if (!r) {
      setDraftOpen(false);
      return;
    }
    let duplicate = false;
    const ok = await change((cur) => {
      if (has(cur, r)) {
        duplicate = true;
        return null;
      }
      return [...cur, r];
    }, t("已添加拦截规则「{r}」", { r }));
    if (duplicate) {
      pushToast("info", t("规则「{r}」已存在，未重复添加", { r }));
      return;
    }
    if (ok) {
      setDraft("");
      setDraftOpen(false);
    }
  };

  const editRule = async (orig: string, next: string): Promise<boolean> => {
    const r = next.trim();
    if (!r || r === orig) return false;
    let duplicate = false;
    const ok = await change((cur) => {
      if (has(cur, r, orig)) {
        duplicate = true;
        return null;
      }
      return cur.map((x) => (x === orig ? r : x));
    }, t("已修改拦截规则「{r}」", { r }));
    if (duplicate) pushToast("info", t("规则「{r}」已存在，未修改", { r }));
    return ok;
  };

  const removeRule = async (rule: string) => {
    const confirmed = await ask(t("确定要删除拦截规则「{rule}」吗？\n删除后，命中该规则的命令将不再需要确认或被拒绝。", { rule }), {
      title: t("删除拦截规则"),
      kind: "warning",
    });
    if (!confirmed) return;
    await change((cur) => cur.filter((x) => x !== rule), t("已删除拦截规则「{rule}」", { rule }));
  };

  return (
    <section id="settings-ai-rules" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("AI 拦截规则")}</span>
        {aiPerm && (
          <span className={`nx-badge ${rules.length ? "nx-badge-amber" : ""}`}>{t("{length} 条", { length: rules.length })}</span>
        )}
        {saving && (
          <span className="nx-hint" role="status">
            {t("正在保存…")}
          </span>
        )}
      </div>
      <p className="nx-hint mb-3.5">
        {t("命中命令、路径或 SQL 时，读写和自动执行模式要求确认，只读和无人值守模式直接拒绝。按整词匹配，不区分大小写。修改后立即保存。")}
      </p>

      {loadError && (
        <div role="alert" className="nx-alert nx-alert-danger mb-2 flex flex-wrap items-center gap-2">
          <span className="min-w-0 flex-1 break-words">{t("读取拦截规则失败：{loadError}", { loadError })}</span>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={load}>
            {t("重试")}
          </button>
        </div>
      )}

      {rules.length > 0 && (
        <input
          className="nx-input nx-input-sm mb-1.5 w-full font-mono"
          aria-label={t("搜索拦截规则")}
          placeholder={t("搜索规则")}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setPage(0);
          }}
        />
      )}
      <div className="mb-1.5 flex flex-col gap-1">
        {pageRules.map((r) => (
          <div key={r} className="flex items-center gap-1">
            <input
              className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
              aria-label={t("拦截规则 {r}", { r })}
              defaultValue={r}
              title={r}
              disabled={saving}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !isImeKeyEvent(e)) {
                  e.preventDefault();
                  e.currentTarget.blur();
                }
              }}
              onBlur={(e) => {
                const input = e.currentTarget;
                void editRule(r, input.value).then((ok) => {
                  if (!ok && input.isConnected) input.value = r;
                });
              }}
            />
            <button
              type="button"
              className="nx-icon-btn"
              title={t("删除规则")}
              aria-label={t("删除拦截规则 {r}", { r })}
              disabled={saving}
              onClick={() => void removeRule(r)}
            >
              <IconClose size={11} />
            </button>
          </div>
        ))}
        {draftOpen && (
          <input
            autoFocus
            className="nx-input nx-input-sm w-full font-mono"
            aria-label={t("新拦截规则")}
            placeholder={t("例如 kubectl delete，Enter 保存，Esc 取消")}
            value={draft}
            disabled={saving}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !isImeKeyEvent(e)) {
                e.preventDefault();
                void addRule();
              }
              if (e.key === "Escape") {
                setDraft("");
                setDraftOpen(false);
              }
            }}
          />
        )}
        {aiPerm && rules.length === 0 && !draftOpen && <div className="nx-hint text-[11px]">{t("暂无拦截规则")}</div>}
        {aiPerm && rules.length > 0 && filtered.length === 0 && (
          <div className="nx-hint text-[11px]">{t("无匹配的规则")}</div>
        )}
      </div>
      {filtered.length > RULE_PAGE_SIZE && (
        <div className="mb-1.5 flex items-center gap-2 text-[11px] text-neutral-500">
          <span>
            {t("第 {value} / {pageCount} 页", { value: currentPage + 1, pageCount })}
          </span>
          <div className="nx-spacer" />
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={currentPage === 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            {t("上一页")}
          </button>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={currentPage >= pageCount - 1}
            onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
          >
            {t("下一页")}
          </button>
        </div>
      )}
      <button
        type="button"
        className="nx-btn nx-btn-outline nx-btn-sm"
        disabled={!aiPerm || saving}
        onClick={() => setDraftOpen(true)}
      >
        <IconPlus size={11} />
        {t("添加规则")}
      </button>

      <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
        {t("不可逆操作（格式化磁盘、清空系统目录、删除数据库）将被直接拒绝。")}
      </p>
    </section>
  );
}
