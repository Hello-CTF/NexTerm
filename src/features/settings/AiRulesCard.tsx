import { useEffect, useMemo, useRef, useState } from "react";
import { aiApi, type AiPermissionConfig } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import { IconClose, IconPlus, IconShield } from "../../ui/icons";
import { showSettingsSection } from "./settingsNavigation";

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
        pushToast("error", `保存拦截规则失败：${describeError(e)}。规则未改动，请重试。`);
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
    }, `已添加拦截规则「${r}」`);
    if (duplicate) {
      pushToast("info", `规则「${r}」已存在，未重复添加`);
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
    }, `已修改拦截规则「${r}」`);
    if (duplicate) pushToast("info", `规则「${r}」已存在，未修改`);
    return ok;
  };

  const removeRule = async (rule: string) => {
    const confirmed = await ask(`确定要删除拦截规则「${rule}」吗？\n删除后，命中该规则的命令将不再需要确认或被拒绝。`, {
      title: "删除拦截规则",
      kind: "warning",
    });
    if (!confirmed) return;
    await change((cur) => cur.filter((x) => x !== rule), `已删除拦截规则「${rule}」`);
  };

  return (
    <section id="settings-ai-rules" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">AI 拦截规则</span>
        {aiPerm && (
          <span className={`nx-badge ${rules.length ? "nx-badge-amber" : ""}`}>{rules.length} 条</span>
        )}
        {saving && (
          <span className="nx-hint" role="status">
            正在保存…
          </span>
        )}
      </div>
      <p className="nx-hint mb-3.5">
        命中命令、路径或 SQL 时，读写和自动执行模式要求确认，只读和无人值守模式直接拒绝。按整词匹配，不区分大小写。修改后立即保存。
      </p>

      {loadError && (
        <div role="alert" className="nx-alert nx-alert-danger mb-2 flex flex-wrap items-center gap-2">
          <span className="min-w-0 flex-1 break-words">读取拦截规则失败：{loadError}</span>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={load}>
            重试
          </button>
        </div>
      )}

      {rules.length > 0 && (
        <input
          className="nx-input nx-input-sm mb-1.5 w-full font-mono"
          aria-label="搜索拦截规则"
          placeholder="搜索规则"
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
              aria-label={`拦截规则 ${r}`}
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
              title="删除规则"
              aria-label={`删除拦截规则 ${r}`}
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
            aria-label="新拦截规则"
            placeholder="例如 kubectl delete，Enter 保存，Esc 取消"
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
        {aiPerm && rules.length === 0 && !draftOpen && <div className="nx-hint text-[11px]">暂无拦截规则</div>}
        {aiPerm && rules.length > 0 && filtered.length === 0 && (
          <div className="nx-hint text-[11px]">无匹配的规则</div>
        )}
      </div>
      {filtered.length > RULE_PAGE_SIZE && (
        <div className="mb-1.5 flex items-center gap-2 text-[11px] text-neutral-500">
          <span>
            第 {currentPage + 1} / {pageCount} 页
          </span>
          <div className="nx-spacer" />
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={currentPage === 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            上一页
          </button>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={currentPage >= pageCount - 1}
            onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
          >
            下一页
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
        添加规则
      </button>

      <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
        不可逆操作（格式化磁盘、清空系统目录、删除数据库）将被直接拒绝。
      </p>
    </section>
  );
}
