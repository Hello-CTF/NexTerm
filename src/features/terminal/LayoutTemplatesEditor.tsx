import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { assetApi } from "../../ipc/commands";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import { assetIcon, IconEdit, IconPlay, IconTrash } from "../../ui/icons";
import {
  deleteLayoutTemplate,
  LAYOUT_TEMPLATE_LIMITS,
  layoutTemplateError,
  layoutTemplatePanes,
  updateLayoutTemplate,
  useLayoutTemplates,
  withPaneSnippet,
  workspaceTreeFromTemplate,
  type LayoutTemplate,
  type LayoutTemplateNode,
} from "../../app/layoutTemplates";
import { openLayoutTemplate } from "../../app/layoutTemplateOpen";
import { paneRects } from "../../app/splitTree";
import { t } from "../../i18n";

// A short choice of asset icons for templates; any asset icon name stays valid when stored by another client.
const TEMPLATE_ICONS = ["Terminal", "Server", "Database", "Code", "Activity", "Bug", "GitBranch", "Cloud", "Monitor", "Shield"] as const;

function LayoutPreview({ layout }: { layout: LayoutTemplateNode }) {
  const rects = useMemo(() => {
    const ids = layoutTemplatePanes(layout).map((_, index) => `p${index}`);
    return paneRects(workspaceTreeFromTemplate(layout, ids));
  }, [layout]);
  return (
    <span
      className="relative block h-9 w-14 shrink-0 overflow-hidden rounded border border-neutral-700/70 bg-neutral-950/40"
      role="img"
      aria-label={t("{count} 个窗格的布局", { count: rects.length })}
    >
      {rects.map(({ paneId, rect }, index) => (
        <span
          key={paneId}
          className="absolute flex items-center justify-center border border-neutral-600/70 text-[9px] text-neutral-400"
          style={{
            left: `${rect.x * 100}%`,
            top: `${rect.y * 100}%`,
            width: `${rect.width * 100}%`,
            height: `${rect.height * 100}%`,
          }}
        >
          {index + 1}
        </span>
      ))}
    </span>
  );
}

/** Lists, opens, edits and deletes workspace layout templates; templates are saved from a host workspace. */
export function LayoutTemplatesEditor() {
  const templates = useLayoutTemplates();
  const [draft, setDraft] = useState<LayoutTemplate | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const assets = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const snippets = useQuery({ queryKey: ["snippets"], queryFn: () => assetApi.snippetList(), refetchOnWindowFocus: false });

  const assetName = (assetId: string): string | null => assets.data?.find((asset) => asset.id === assetId)?.name ?? null;
  const assetMissing = (assetId: string): boolean => assets.isSuccess && !assets.data.some((asset) => asset.id === assetId);

  const run = async (action: () => Promise<void>): Promise<boolean> => {
    setSaving(true);
    try {
      await action();
      setError(null);
      return true;
    } catch (cause) {
      setError(t("保存失败：{value}", { value: describeError(cause) }));
      return false;
    } finally {
      setSaving(false);
    }
  };

  const remove = async (template: LayoutTemplate) => {
    if (!(await ask(t("删除布局模板「{name}」？", { name: template.name }), { kind: "warning" }))) return;
    await run(() => deleteLayoutTemplate(template.id));
  };

  const submitDraft = async () => {
    if (!draft) return;
    const next: LayoutTemplate = {
      ...draft,
      name: draft.name.trim(),
      description: draft.description?.trim() || undefined,
    };
    const invalid = layoutTemplateError(next);
    if (invalid) {
      setError(invalid);
      return;
    }
    if (await run(() => updateLayoutTemplate(next))) setDraft(null);
  };

  const snippetOptions = [
    { value: "", label: t("不运行片段") },
    ...(snippets.data ?? []).map((snippet) => ({ value: snippet.id, label: snippet.name })),
  ];

  return (
    <div className="flex flex-col gap-2" aria-busy={saving}>
      {templates.length === 0 && (
        <p className="nx-hint">{t("暂无布局模板。在主机工作区标签上右键，或在跳转面板中选择「保存当前布局为模板」。")}</p>
      )}
      {templates.length > 0 && (
        <ul className="flex flex-col gap-1" aria-label={t("布局模板列表")}>
          {templates.map((template) => {
            const Icon = assetIcon("ssh", template.icon);
            const host = assetName(template.assetId);
            const missing = assetMissing(template.assetId);
            const paneCount = layoutTemplatePanes(template.layout).length;
            return (
              <li
                key={template.id}
                className="flex items-center gap-2 rounded-md border border-neutral-800/60 bg-neutral-900/50 px-2 py-1.5"
              >
                <Icon size={14} className="shrink-0 text-neutral-400" />
                <LayoutPreview layout={template.layout} />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-[12.5px] text-neutral-200">{template.name}</span>
                  <span className="truncate text-[11px] text-neutral-500">
                    {[host, t("{count} 个窗格", { count: paneCount }), template.description].filter(Boolean).join(" · ")}
                  </span>
                </span>
                {missing && <span className="nx-badge nx-badge-amber shrink-0">{t("主机已不存在")}</span>}
                <button
                  type="button"
                  className="nx-icon-btn nx-icon-btn-sm shrink-0"
                  aria-label={t("打开 {name}", { name: template.name })}
                  disabled={saving}
                  onClick={() => void openLayoutTemplate(template)}
                >
                  <IconPlay size={12} />
                </button>
                <button
                  type="button"
                  className="nx-icon-btn nx-icon-btn-sm shrink-0"
                  aria-label={t("编辑 {name}", { name: template.name })}
                  disabled={saving}
                  onClick={() => {
                    setError(null);
                    setDraft(template);
                  }}
                >
                  <IconEdit size={12} />
                </button>
                <button
                  type="button"
                  className="nx-icon-btn nx-icon-btn-sm shrink-0"
                  aria-label={t("删除 {name}", { name: template.name })}
                  disabled={saving}
                  onClick={() => void remove(template)}
                >
                  <IconTrash size={12} />
                </button>
              </li>
            );
          })}
        </ul>
      )}
      {draft && (
        <form
          className="flex flex-col gap-2 rounded-md border border-neutral-800/60 p-2"
          aria-label={t("编辑布局模板")}
          onSubmit={(event) => {
            event.preventDefault();
            void submitDraft();
          }}
        >
          <label className="flex flex-col gap-1">
            <span className="nx-setting-label">{t("名称")}</span>
            <input
              className="nx-input"
              value={draft.name}
              maxLength={LAYOUT_TEMPLATE_LIMITS.nameChars * 2}
              onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="nx-setting-label">{t("说明（可选）")}</span>
            <input
              className="nx-input"
              value={draft.description ?? ""}
              maxLength={LAYOUT_TEMPLATE_LIMITS.descriptionChars * 2}
              onChange={(event) => setDraft({ ...draft, description: event.target.value })}
            />
          </label>
          <div className="flex flex-col gap-1">
            <span className="nx-setting-label">{t("图标")}</span>
            <div className="flex flex-wrap gap-1" role="radiogroup" aria-label={t("图标")}>
              {TEMPLATE_ICONS.map((name) => {
                const Icon = assetIcon("ssh", name);
                const selected = (draft.icon ?? "") === name;
                return (
                  <button
                    key={name}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    aria-label={name}
                    className={`nx-icon-btn nx-icon-btn-sm ${selected ? "is-active" : ""}`}
                    onClick={() => setDraft({ ...draft, icon: selected ? undefined : name })}
                  >
                    <Icon size={13} />
                  </button>
                );
              })}
            </div>
          </div>
          <div className="flex flex-col gap-1">
            <span className="nx-setting-label">{t("各窗格的启动片段")}</span>
            <p className="nx-hint">{t("每个窗格打开一个终端；片段在终端就绪并经你确认后运行。")}</p>
            {layoutTemplatePanes(draft.layout).map((pane, index) => (
              <div key={index} className="flex items-center gap-2">
                <span className="w-14 shrink-0 text-[12px] text-neutral-400">{t("窗格 {pane}", { pane: index + 1 })}</span>
                <Select
                  className="min-w-0 flex-1"
                  ariaLabel={t("窗格 {pane} 的启动片段", { pane: index + 1 })}
                  value={pane.snippetId ?? ""}
                  options={
                    pane.snippetId && snippets.isSuccess && !snippets.data.some((snippet) => snippet.id === pane.snippetId)
                      ? [...snippetOptions, { value: pane.snippetId, label: t("片段已不存在") }]
                      : snippetOptions
                  }
                  onChange={(value) => setDraft({ ...draft, layout: withPaneSnippet(draft.layout, index, value || undefined) })}
                />
              </div>
            ))}
          </div>
          <div className="flex justify-end gap-2">
            <button
              type="button"
              className="nx-btn nx-btn-ghost"
              onClick={() => {
                setError(null);
                setDraft(null);
              }}
            >
              {t("取消")}
            </button>
            <button type="submit" className="nx-btn nx-btn-primary" disabled={saving}>
              {t("保存")}
            </button>
          </div>
        </form>
      )}
      {error && (
        <p role="alert" className="text-[12px] text-red-400">
          {error}
        </p>
      )}
    </div>
  );
}
