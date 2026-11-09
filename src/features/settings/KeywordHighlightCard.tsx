import { Fragment, useMemo, useState, useSyncExternalStore, type CSSProperties, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { assetApi } from "../../ipc/commands";
import { getResolvedTerminalTheme, subscribeAppearancePrefs } from "../../app/preferences";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import { IconArrowDown, IconArrowUp, IconEdit, IconPlus, IconStar, IconTrash, IconXCircle } from "../../ui/icons";
import { useHostLabeler } from "../hosts/hostLabel";
import {
  compileKeywordHighlights,
  HIGHLIGHT_COLORS,
  highlightBackground,
  highlightColorLabel,
  highlightForeground,
  highlightRuleError,
  KEYWORD_HIGHLIGHT_LIMITS,
  KEYWORD_HIGHLIGHT_PRESETS,
  matchKeywordHighlights,
  newHighlightRuleId,
  saveKeywordHighlights,
  useKeywordHighlights,
  type HighlightColor,
  type HighlightStyle,
  type KeywordHighlightRule,
  type KeywordHighlightSettings,
} from "../terminal/keywordHighlightRules";
import { SyncScopeBadge } from "./SyncScopeBadge";
import { t } from "../../i18n";

const SAMPLE_TEXT = [
  "2026-11-09 10:02:11 INFO  service started on 10.0.0.12:8080",
  "2026-11-09 10:02:13 WARN  disk usage at 91%",
  "2026-11-09 10:02:15 ERROR connection failed: permission denied",
  "request 123e4567-e89b-12d3-a456-426614174000 OK",
  "部署完成，测试全部 passed",
].join("\n");

function useTerminalTheme(): "dark" | "light" {
  return useSyncExternalStore(subscribeAppearancePrefs, getResolvedTerminalTheme);
}

function styleCss(style: HighlightStyle, theme: "dark" | "light"): CSSProperties {
  return {
    ...(style.foreground ? { color: highlightForeground(style.foreground, theme) } : {}),
    ...(style.background ? { backgroundColor: highlightBackground(style.background, theme) } : {}),
    ...(style.underline ? { textDecoration: "underline" } : {}),
  };
}

/** Renders sample text with the highlights the given settings produce, as a terminal would show them. */
function HighlightPreview({ settings, text, theme }: { settings: KeywordHighlightSettings; text: string; theme: "dark" | "light" }) {
  const rules = useMemo(() => compileKeywordHighlights(settings), [settings]);
  return (
    <pre
      className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-md border border-[var(--nx-border)] p-2 font-mono text-[12px] leading-5"
      style={theme === "dark" ? { background: "#141414", color: "#e5e5e5" } : { background: "#fafafa", color: "#262626" }}
      data-testid="keyword-highlight-preview"
    >
      {text.split("\n").map((line, index) => {
        const parts: ReactNode[] = [];
        let at = 0;
        for (const match of matchKeywordHighlights(line, rules)) {
          if (match.start > at) parts.push(line.slice(at, match.start));
          parts.push(
            <mark key={match.start} style={{ background: "transparent", color: "inherit", ...styleCss(match.style, theme) }}>
              {line.slice(match.start, match.end)}
            </mark>,
          );
          at = match.end;
        }
        if (at < line.length) parts.push(line.slice(at));
        return (
          <Fragment key={index}>
            {parts}
            {"\n"}
          </Fragment>
        );
      })}
    </pre>
  );
}

const colorOptions = (none: string) => [
  { value: "", label: none },
  ...HIGHLIGHT_COLORS.map((color) => ({ value: color, label: highlightColorLabel(color) })),
];

const emptyRule = (): KeywordHighlightRule => ({
  id: newHighlightRuleId(),
  pattern: "",
  regex: false,
  caseSensitive: false,
  wholeWord: true,
  foreground: "red",
  background: "",
  underline: false,
  enabled: true,
});

function RuleForm({
  draft,
  onChange,
  hosts,
  error,
  saving,
  onSubmit,
  onCancel,
  isNew,
}: {
  draft: KeywordHighlightRule;
  onChange: (rule: KeywordHighlightRule) => void;
  hosts: readonly { id: string; name: string }[];
  error: string | null;
  saving: boolean;
  onSubmit: () => void;
  onCancel: () => void;
  isNew: boolean;
}) {
  const scoped = draft.assetIds !== undefined;
  const flag = (key: "regex" | "caseSensitive" | "wholeWord" | "underline", label: string) => (
    <label className="flex cursor-pointer items-center gap-2 text-[12.5px] text-neutral-200">
      <input type="checkbox" className="h-4 w-4" checked={draft[key]} onChange={(event) => onChange({ ...draft, [key]: event.target.checked })} />
      {label}
    </label>
  );
  return (
    <form
      className="flex flex-col gap-2 rounded-md border border-[var(--nx-border)] p-3"
      aria-label={isNew ? t("新建高亮规则") : t("编辑高亮规则")}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <label className="flex flex-col gap-1">
        <span className="nx-setting-label">{t("匹配内容")}</span>
        <input
          className="nx-input font-mono"
          value={draft.pattern}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          placeholder={draft.regex ? "timeout|refused" : "ERROR"}
          aria-invalid={error !== null}
          onChange={(event) => onChange({ ...draft, pattern: event.target.value })}
        />
      </label>
      <div className="flex flex-wrap gap-x-4 gap-y-1.5">
        {flag("regex", t("正则表达式"))}
        {flag("caseSensitive", t("区分大小写"))}
        {flag("wholeWord", t("全词匹配"))}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Select
          className="w-full sm:w-36"
          ariaLabel={t("文字颜色")}
          value={draft.foreground}
          options={colorOptions(t("文字颜色：不变"))}
          onChange={(value) => onChange({ ...draft, foreground: value as HighlightColor | "" })}
        />
        <Select
          className="w-full sm:w-36"
          ariaLabel={t("背景颜色")}
          value={draft.background}
          options={colorOptions(t("背景颜色：不变"))}
          onChange={(value) => onChange({ ...draft, background: value as HighlightColor | "" })}
        />
        {flag("underline", t("下划线"))}
      </div>
      <div className="flex flex-col gap-1.5">
        <Select
          className="w-full sm:w-48"
          ariaLabel={t("生效范围")}
          value={scoped ? "hosts" : "all"}
          options={[
            { value: "all", label: t("所有会话") },
            { value: "hosts", label: t("仅指定主机") },
          ]}
          onChange={(value) => {
            const next = { ...draft };
            if (value === "hosts") next.assetIds = draft.assetIds ?? [];
            else delete next.assetIds;
            onChange(next);
          }}
        />
        {scoped && (
          <div className="flex max-h-40 flex-col gap-1 overflow-auto" role="group" aria-label={t("选择主机")}>
            {hosts.length === 0 && <span className="nx-hint">{t("暂无主机")}</span>}
            {hosts.map((host) => (
              <label key={host.id} className="flex cursor-pointer items-center gap-2 text-[12.5px] text-neutral-200">
                <input
                  type="checkbox"
                  className="h-4 w-4"
                  checked={draft.assetIds?.includes(host.id) ?? false}
                  onChange={(event) => {
                    const current = draft.assetIds ?? [];
                    onChange({
                      ...draft,
                      assetIds: event.target.checked ? [...current, host.id] : current.filter((id) => id !== host.id),
                    });
                  }}
                />
                {host.name}
              </label>
            ))}
          </div>
        )}
      </div>
      {error && (
        <div className="nx-alert nx-alert-danger text-[12px]" role="alert">
          {error}
        </div>
      )}
      <div className="flex justify-end gap-2">
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onCancel}>
          {t("取消")}
        </button>
        <button type="submit" className="nx-btn nx-btn-primary nx-btn-sm" disabled={saving}>
          {saving ? t("正在保存…") : t("保存")}
        </button>
      </div>
    </form>
  );
}

function presetStyle(rule: Pick<KeywordHighlightRule, "foreground" | "background" | "underline">): HighlightStyle {
  return { foreground: rule.foreground || undefined, background: rule.background || undefined, underline: rule.underline };
}

function ruleSummary(rule: KeywordHighlightRule): string {
  const flags = [rule.regex ? t("正则") : t("文本")];
  if (rule.caseSensitive) flags.push(t("区分大小写"));
  if (rule.wholeWord) flags.push(t("全词"));
  return flags.join(" · ");
}

export function KeywordHighlightCard() {
  const settings = useKeywordHighlights();
  const theme = useTerminalTheme();
  const hostName = useHostLabeler();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const hosts = useMemo(() => (assetsQuery.data ?? []).map((asset) => ({ id: asset.id, name: asset.name })), [assetsQuery.data]);
  const [draft, setDraft] = useState<{ rule: KeywordHighlightRule; index: number | null } | null>(null);
  const [draftError, setDraftError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [sample, setSample] = useState(SAMPLE_TEXT);

  const save = async (next: KeywordHighlightSettings): Promise<boolean> => {
    setSaving(true);
    try {
      await saveKeywordHighlights(next);
      setError(null);
      return true;
    } catch (cause) {
      setError(t("保存失败：{value}", { value: describeError(cause) }));
      return false;
    } finally {
      setSaving(false);
    }
  };

  const togglePreset = (id: string, on: boolean) =>
    void save({ ...settings, presets: on ? [...settings.presets, id] : settings.presets.filter((preset) => preset !== id) });
  const replaceRules = (rules: KeywordHighlightRule[]) => save({ ...settings, rules });
  const move = (index: number, delta: -1 | 1) => {
    const target = index + delta;
    if (target < 0 || target >= settings.rules.length) return;
    const rules = [...settings.rules];
    [rules[index], rules[target]] = [rules[target], rules[index]];
    void replaceRules(rules);
  };

  const submitDraft = async () => {
    if (!draft) return;
    const rule = { ...draft.rule };
    const invalid = highlightRuleError(rule);
    if (invalid) {
      setDraftError(invalid);
      return;
    }
    if (rule.assetIds && rule.assetIds.length === 0) {
      setDraftError(t("请至少选择一台主机"));
      return;
    }
    const rules = draft.index === null ? [...settings.rules, rule] : settings.rules.map((existing, i) => (i === draft.index ? rule : existing));
    if (await replaceRules(rules)) {
      setDraft(null);
      setDraftError(null);
    }
  };

  // The preview shows the draft in its place, so its effect is visible before saving.
  const previewSettings = useMemo<KeywordHighlightSettings>(() => {
    if (!draft) return settings;
    const rules = draft.index === null ? [...settings.rules, draft.rule] : settings.rules.map((rule, i) => (i === draft.index ? draft.rule : rule));
    return { ...settings, rules: rules.map((rule) => (rule.assetIds ? { ...rule, assetIds: undefined } : rule)) };
  }, [settings, draft]);

  const full = settings.rules.length >= KEYWORD_HIGHLIGHT_LIMITS.rules;

  return (
    <section id="settings-keyword-highlight" className="nx-card scroll-mt-12" aria-busy={saving}>
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconStar size={15} className="text-neutral-400" />
        <span className="nx-card-title" data-setting="keyword-highlight">{t("关键词高亮")}</span>
        <SyncScopeBadge scope="sync" />
      </div>
      <p className="nx-hint mb-3">
        {t("终端输出中匹配的文字按规则着色。规则按顺序匹配，重叠时靠前的规则优先，预设排在自定义规则之后；全屏程序（如 vim）和大量输出期间不高亮。")}
      </p>

      {error && (
        <div className="nx-alert nx-alert-danger mb-2 flex items-start gap-2 text-[12px]" role="alert">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
        </div>
      )}

      <div className="nx-setting-label mb-1.5" data-setting="keyword-highlight-presets">{t("内置预设")}</div>
      <ul className="mb-3 flex flex-col gap-1" aria-label={t("内置预设")}>
        {KEYWORD_HIGHLIGHT_PRESETS.map((preset) => (
          <li key={preset.id}>
            <label className="flex cursor-pointer items-center gap-2 text-[12.5px] text-neutral-200">
              <input
                type="checkbox"
                className="h-4 w-4"
                checked={settings.presets.includes(preset.id)}
                disabled={saving}
                onChange={(event) => togglePreset(preset.id, event.target.checked)}
              />
              <span>{preset.label}</span>
              <span className="truncate font-mono text-[11px]" style={styleCss(presetStyle(preset.rules[0]), theme)}>
                {preset.description}
              </span>
            </label>
          </li>
        ))}
      </ul>

      <div className="nx-setting-label mb-1.5">{t("自定义规则")}</div>
      {settings.rules.length === 0 && !draft && <p className="nx-hint mb-2">{t("暂无自定义规则。")}</p>}
      {settings.rules.length > 0 && (
        <ol className="mb-2 flex flex-col gap-1" aria-label={t("高亮规则")}>
          {settings.rules.map((rule, index) => {
            const invalid = highlightRuleError(rule);
            return (
              <li key={rule.id} className="flex items-center gap-2 rounded-md border border-neutral-800/60 px-2 py-1.5" data-rule-id={rule.id}>
                <input
                  type="checkbox"
                  className="h-4 w-4 shrink-0"
                  aria-label={t("启用 {name}", { name: rule.pattern })}
                  checked={rule.enabled}
                  disabled={saving}
                  onChange={() => void replaceRules(settings.rules.map((r, i) => (i === index ? { ...r, enabled: !r.enabled } : r)))}
                />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span
                    className="truncate font-mono text-[12.5px]"
                    style={styleCss(presetStyle(rule), theme)}
                  >
                    {rule.pattern}
                  </span>
                  <span className="nx-hint truncate text-[11px]">
                    {ruleSummary(rule)} ·{" "}
                    {rule.assetIds ? t("仅 {value}", { value: rule.assetIds.map((id) => hostName(id)).join("、") }) : t("所有会话")}
                  </span>
                  {invalid && <span className="text-[11px] text-[var(--nx-fg-warning)]">{t("未生效：{error}", { error: invalid })}</span>}
                </span>
                <span className="flex shrink-0 items-center gap-0.5">
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label={t("上移 {label}", { label: rule.pattern })} disabled={index === 0 || saving} onClick={() => move(index, -1)}>
                    <IconArrowUp size={11} />
                  </button>
                  <button
                    type="button"
                    className="nx-icon-btn nx-icon-btn-sm"
                    aria-label={t("下移 {label}", { label: rule.pattern })}
                    disabled={index === settings.rules.length - 1 || saving}
                    onClick={() => move(index, 1)}
                  >
                    <IconArrowDown size={11} />
                  </button>
                  <button
                    type="button"
                    className="nx-icon-btn nx-icon-btn-sm"
                    aria-label={t("编辑 {label}", { label: rule.pattern })}
                    disabled={saving || draft !== null}
                    onClick={() => {
                      setDraftError(null);
                      setDraft({ rule: { ...rule }, index });
                    }}
                  >
                    <IconEdit size={12} />
                  </button>
                  <button
                    type="button"
                    className="nx-icon-btn nx-icon-btn-sm"
                    aria-label={t("删除 {label}", { label: rule.pattern })}
                    disabled={saving}
                    onClick={() => void replaceRules(settings.rules.filter((_, i) => i !== index))}
                  >
                    <IconTrash size={12} />
                  </button>
                </span>
              </li>
            );
          })}
        </ol>
      )}
      {draft ? (
        <div className="mb-3">
          <RuleForm
            draft={draft.rule}
            isNew={draft.index === null}
            onChange={(rule) => setDraft({ ...draft, rule })}
            hosts={hosts}
            error={draftError}
            saving={saving}
            onSubmit={() => void submitDraft()}
            onCancel={() => {
              setDraft(null);
              setDraftError(null);
            }}
          />
        </div>
      ) : (
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-sm mb-3"
          disabled={full || saving}
          onClick={() => {
            setDraftError(null);
            setDraft({ rule: emptyRule(), index: null });
          }}
        >
          <IconPlus size={11} />
          {t("新建规则")}
        </button>
      )}
      {full && <p className="nx-hint mb-3">{t("最多保存 {count} 条高亮规则", { count: KEYWORD_HIGHLIGHT_LIMITS.rules })}</p>}

      <div className="flex flex-col gap-1.5" data-setting="keyword-highlight-preview">
        <label className="nx-setting-label" htmlFor="keyword-highlight-sample">
          {t("预览")}
        </label>
        <textarea
          id="keyword-highlight-sample"
          className="nx-textarea font-mono text-[12px]"
          rows={3}
          value={sample}
          spellCheck={false}
          onChange={(event) => setSample(event.target.value)}
        />
        <HighlightPreview settings={previewSettings} text={sample} theme={theme} />
      </div>
    </section>
  );
}
