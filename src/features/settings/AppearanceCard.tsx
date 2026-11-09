import { useEffect, useId, useRef, useState } from "react";
import type { SettingsPanelId } from "./settingsIndex";
import { useUi } from "../../app/store";
import type { ThemeMode } from "../../app/theme";
import {
  getAppearancePrefs,
  resetAppearancePrefs,
  setAccent,
  setTerminalContrast,
  setTerminalCustomSchemes,
  setTerminalScheme,
  setTerminalFontFamily,
  setTerminalFontSize,
  setTerminalTheme,
  setUiFontSize,
  TERMINAL_FONT_FAMILIES,
  TERMINAL_FONT_SIZE_MAX,
  TERMINAL_FONT_SIZE_MIN,
  UI_FONT_PRESETS,
  UI_FONT_SIZE_MAX,
  UI_FONT_SIZE_MIN,
  uiFontSize,
  useAppearancePrefs,
  useResolvedTerminalTheme,
  type TerminalContrast,
  type TerminalFontFamily,
  type ResolvedTerminalTheme,
  type TerminalThemePref,
} from "../../app/preferences";
import { ACCENT_PRESETS, DEFAULT_ACCENT, type AccentPreset } from "../../app/accentPresets";
import {
  DEFAULT_DARK_SCHEME_ID,
  DEFAULT_LIGHT_SCHEME_ID,
  terminalSchemeCatalog,
  type TerminalScheme,
} from "../terminal/xtermTheme";
import {
  importSchemeFile,
  MAX_CUSTOM_SCHEMES,
  MAX_SCHEME_IMPORT_BYTES,
  MAX_SCHEME_NAME_LENGTH,
  newCustomSchemeId,
  normalizeSchemeName,
  schemeKind,
  SchemeImportError,
  type CustomTerminalScheme,
  type SchemePalette,
} from "../terminal/terminalSchemes";
import { IconChevronDown, IconChevronRight, IconMinus, IconMonitor, IconPlus, IconRefresh, IconTrash, IconUpload } from "../../ui/icons";
import { Select } from "../../ui/Select";
import { SyncScopeBadge } from "./SyncScopeBadge";
import { LOCALE_NAMES, LOCALES, setLocale, storedLocale, t, type Locale } from "../../i18n";

const FOLLOW_SYSTEM = "system";

const THEME_OPTIONS: { value: ThemeMode; label: string }[] = [
  { value: "system", label: t("跟随系统") },
  { value: "light", label: t("浅色") },
  { value: "dark", label: t("深色") },
];

const TERM_THEME_OPTIONS: { value: TerminalThemePref; label: string }[] = [
  { value: "dark", label: t("深色") },
  { value: "light", label: t("浅色") },
  { value: "interface", label: t("跟随界面") },
];

const TERM_CONTRAST_OPTIONS: { value: TerminalContrast; label: string }[] = [
  { value: "standard", label: t("标准") },
  { value: "high", label: t("高") },
  { value: "maximum", label: t("极高") },
];

const TERM_FONT_LABELS: Record<TerminalFontFamily, string> = {
  cascadia: t("Cascadia（默认）"),
  system: t("系统等宽"),
  "jetbrains-mono": "JetBrains Mono",
  "fira-code": "Fira Code",
  "source-code-pro": "Source Code Pro",
  menlo: "Menlo",
  consolas: "Consolas",
};

const ACCENT_LABELS: Record<AccentPreset, string> = {
  indigo: t("靛蓝（默认）"),
  blue: t("蓝色"),
  teal: t("青色"),
  green: t("绿色"),
  violet: t("紫色"),
};

const SCHEME_SLOTS: { kind: ResolvedTerminalTheme; label: string }[] = [
  { kind: "dark", label: t("深色终端配色") },
  { kind: "light", label: t("浅色终端配色") },
];

function SchemePreview({ palette }: { palette: SchemePalette }) {
  const ansi = palette.ansi;
  return (
    <div
      aria-hidden="true"
      className="overflow-hidden rounded-[4px] px-1.5 py-1 font-mono text-[9.5px] leading-[1.4]"
      style={{ background: palette.background, color: palette.foreground }}
    >
      <div className="truncate">
        <span style={{ color: ansi[2] }}>~/app</span> <span style={{ color: ansi[4] }}>$</span> ls
      </div>
      <div className="truncate">
        <span style={{ color: ansi[12] }}>src</span> <span style={{ color: ansi[10] }}>run.sh</span>{" "}
        <span style={{ color: ansi[14] }}>lib</span> <span style={{ color: ansi[9] }}>a.tgz</span>
      </div>
      <div className="mt-1 flex gap-[2px]">
        {ansi.slice(1, 7).map((color, i) => (
          <span key={i} className="h-1.5 flex-1 rounded-[1px]" style={{ background: color }} />
        ))}
      </div>
    </div>
  );
}

function SchemeGrid({
  label,
  schemes,
  selected,
  onSelect,
}: {
  label: string;
  schemes: TerminalScheme[];
  selected: string;
  onSelect: (id: string) => void;
}) {
  // An id that resolves to nothing (a scheme from a newer client) shows the NexTerm default as active.
  const activeId = schemes.some((scheme) => scheme.id === selected) ? selected : schemes[0]?.id;
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(112px,1fr))] gap-1.5" role="group" aria-label={label}>
      {schemes.map((scheme) => {
        const active = scheme.id === activeId;
        return (
          <button
            key={scheme.id}
            type="button"
            className={`nx-scheme-swatch ${active ? "is-active" : ""}`}
            aria-pressed={active}
            aria-label={scheme.name}
            title={scheme.name}
            onClick={() => onSelect(scheme.id)}
          >
            <SchemePreview palette={scheme.palette} />
            <span className="block truncate px-0.5 pt-1 text-left text-[11px]">{scheme.name}</span>
          </button>
        );
      })}
    </div>
  );
}

function importErrorText(fileName: string, err: unknown): string {
  if (err instanceof SchemeImportError && err.message === "size") {
    return t("{name}：文件过大", { name: fileName });
  }
  return t("{name}：无法识别，需要背景色、前景色和全部 16 个 ANSI 颜色", { name: fileName });
}

function CustomSchemeRow({
  scheme,
  onRename,
  onDelete,
}: {
  scheme: CustomTerminalScheme;
  onRename: (name: string) => void;
  onDelete: () => void;
}) {
  const [draft, setDraft] = useState(scheme.name);
  useEffect(() => setDraft(scheme.name), [scheme.name]);
  const commit = () => {
    const name = normalizeSchemeName(draft);
    if (name === "" || name === scheme.name) {
      setDraft(scheme.name);
      return;
    }
    onRename(name);
  };
  return (
    <li className="flex items-center gap-2">
      <span
        aria-hidden="true"
        className="h-4 w-4 shrink-0 rounded-[3px] border border-neutral-700"
        style={{ background: scheme.background }}
      />
      <input
        className="nx-input nx-input-sm min-w-0 flex-1"
        aria-label={t("配色名称")}
        maxLength={MAX_SCHEME_NAME_LENGTH}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.currentTarget.blur();
          if (e.key === "Escape") setDraft(scheme.name);
        }}
      />
      <button
        type="button"
        className="nx-btn nx-btn-ghost nx-btn-sm"
        aria-label={t("删除配色 {name}", { name: scheme.name })}
        title={t("删除")}
        onClick={onDelete}
      >
        <IconTrash size={11} />
      </button>
    </li>
  );
}

function formatFontSize(size: number): string {
  return String(Math.round(size * 100) / 100);
}

export function AppearanceCard({ reveal }: { reveal?: { panel: SettingsPanelId; n: number } | null }) {
  const themeMode = useUi((s) => s.themeMode);
  const resolvedTheme = useUi((s) => s.resolvedTheme);
  const setThemeMode = useUi((s) => s.setThemeMode);
  const prefs = useAppearancePrefs();

  const current =
    themeMode === "system"
      ? t("跟随系统 · 当前{value}", { value: resolvedTheme === "dark" ? t("深色") : t("浅色") })
      : themeMode === "light"
        ? t("浅色")
        : t("深色");

  const resolvedTermTheme = useResolvedTerminalTheme();
  const appearanceIsDefault =
    prefs.uiFontPreset === 13 &&
    prefs.uiFontScale === 1 &&
    prefs.terminalFontSize === 13 &&
    prefs.terminalFontFamily === "cascadia" &&
    prefs.terminalTheme === "dark" &&
    prefs.terminalContrast === "standard" &&
    prefs.terminalSchemeDark === DEFAULT_DARK_SCHEME_ID &&
    prefs.terminalSchemeLight === DEFAULT_LIGHT_SCHEME_ID &&
    prefs.accent === DEFAULT_ACCENT;
  const catalog = terminalSchemeCatalog(prefs.terminalCustomSchemes);
  const importInputRef = useRef<HTMLInputElement>(null);
  const [importStatus, setImportStatus] = useState<{ imported: number; problems: string[] } | null>(null);
  const importFiles = async (files: File[]) => {
    let list = [...getAppearancePrefs().terminalCustomSchemes];
    const problems: string[] = [];
    let imported = 0;
    let last: CustomTerminalScheme | null = null;
    for (const file of files) {
      try {
        if (file.size > MAX_SCHEME_IMPORT_BYTES) throw new SchemeImportError("size");
        for (const item of importSchemeFile(file.name, await file.text())) {
          if (list.length >= MAX_CUSTOM_SCHEMES) {
            problems.push(t("{name}：已达到 {max} 个自定义配色上限，未导入", { name: item.name, max: MAX_CUSTOM_SCHEMES }));
            continue;
          }
          const scheme: CustomTerminalScheme = { id: newCustomSchemeId(list), name: item.name, ...item.palette };
          list = [...list, scheme];
          last = scheme;
          imported += 1;
        }
      } catch (err) {
        problems.push(importErrorText(file.name, err));
      }
    }
    if (last) {
      setTerminalCustomSchemes(list);
      setTerminalScheme(schemeKind(last), last.id);
    }
    setImportStatus({ imported, problems });
  };
  const updateCustomSchemes = (next: CustomTerminalScheme[]) => {
    setImportStatus(null);
    setTerminalCustomSchemes(next);
  };
  const advancedPanelId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  useEffect(() => {
    if (reveal?.panel === "appearance-advanced") setAdvancedOpen(true);
  }, [reveal]);

  // A size saved by an older preset/scale pair that matches no preset shows as 自定义 with its effective value, and is
  // only rewritten when the user enters a different size.
  const effectiveFontSize = formatFontSize(uiFontSize(prefs));
  const [customFontOpen, setCustomFontOpen] = useState(false);
  const [customFontDraft, setCustomFontDraft] = useState("");
  const showCustomFont = customFontOpen || !UI_FONT_PRESETS.some((preset) => formatFontSize(preset) === effectiveFontSize);
  useEffect(() => {
    if (showCustomFont) setCustomFontDraft(effectiveFontSize);
  }, [showCustomFont, effectiveFontSize]);
  const commitCustomFont = () => {
    const num = Number(customFontDraft);
    if (customFontDraft.trim() === "" || !Number.isFinite(num) || customFontDraft.trim() === effectiveFontSize) return;
    setUiFontSize(num);
  };

  const resetAll = () => {
    setThemeMode("system");
    resetAppearancePrefs();
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconMonitor size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("外观")}</span>
        <span className="nx-badge">{current}</span>
        <SyncScopeBadge scope="sync" />
      </div>
      <p className="nx-hint mb-3.5">{t("终端与代码编辑器的主题和字号独立于界面设置。")}</p>
      <div className="mb-3.5">
        <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="ui-language">
          {t("界面语言")}
          <SyncScopeBadge scope="local" />
        </div>
        <Select
          className="w-full"
          ariaLabel={t("界面语言")}
          value={storedLocale() ?? FOLLOW_SYSTEM}
          options={[
            { value: FOLLOW_SYSTEM, label: t("跟随系统") },
            ...LOCALES.map((locale) => ({ value: locale, label: LOCALE_NAMES[locale] })),
          ]}
          onChange={(value) => setLocale(value === FOLLOW_SYSTEM ? null : (value as Locale))}
        />
        <div className="nx-hint mt-1.5">{t("切换后界面会重新加载；终端会话不受影响。")}</div>
      </div>
      <div className="nx-setting-label mb-1.5">{t("界面主题")}</div>
      <div className="nx-segment" role="group" aria-label={t("界面主题")} data-setting="ui-theme">
        {THEME_OPTIONS.map((option) => (
          <button
            key={option.value}
            type="button"
            className={`nx-segment-item ${themeMode === option.value ? "is-active" : ""}`}
            aria-pressed={themeMode === option.value}
            onClick={() => setThemeMode(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="ui-accent">{t("强调色")}</div>
        <div className="nx-segment flex-wrap" role="group" aria-label={t("强调色")}>
          {ACCENT_PRESETS.map((preset) => {
            const active = prefs.accent === preset.id;
            return (
              <button
                key={preset.id}
                type="button"
                className={`nx-segment-item ${active ? "is-active" : ""}`}
                aria-pressed={active}
                onClick={() => setAccent(preset.id)}
              >
                <span
                  aria-hidden="true"
                  className="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
                  style={{ background: preset[resolvedTheme === "light" ? "light" : "dark"].accent }}
                />
                {ACCENT_LABELS[preset.id]}
              </button>
            );
          })}
        </div>
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="ui-font-size">{t("界面字号（像素）")}</div>
        <div className="nx-segment flex-wrap" role="group" aria-label={t("界面字号")}>
          {UI_FONT_PRESETS.map((preset) => {
            const active = !showCustomFont && formatFontSize(preset) === effectiveFontSize;
            return (
              <button
                key={preset}
                type="button"
                className={`nx-segment-item ${active ? "is-active" : ""}`}
                aria-pressed={active}
                onClick={() => {
                  setCustomFontOpen(false);
                  setUiFontSize(preset);
                }}
              >
                {preset}
              </button>
            );
          })}
          <button
            type="button"
            className={`nx-segment-item ${showCustomFont ? "is-active" : ""}`}
            aria-pressed={showCustomFont}
            onClick={() => setCustomFontOpen(true)}
          >
            {t("自定义")}
          </button>
        </div>
        {showCustomFont && (
          <input
            type="number"
            className="nx-input nx-input-sm mt-1.5 w-[96px] font-mono"
            aria-label={t("自定义界面字号")}
            min={UI_FONT_SIZE_MIN}
            max={UI_FONT_SIZE_MAX}
            step={0.5}
            value={customFontDraft}
            onChange={(e) => setCustomFontDraft(e.target.value)}
            onBlur={commitCustomFont}
            onKeyDown={(e) => {
              if (e.key === "Enter") e.currentTarget.blur();
            }}
          />
        )}
      </div>

      <div className="mt-3.5">
        <div className="mb-1.5 flex items-center gap-2">
          <span className="nx-setting-label" data-setting="terminal-theme">{t("终端与编辑器主题")}</span>
          {prefs.terminalTheme === "interface" && (
            <span className="nx-badge">{t("当前{value}", { value: resolvedTermTheme === "dark" ? t("深色") : t("浅色") })}</span>
          )}
        </div>
        <div className="nx-segment" role="group" aria-label={t("终端与编辑器主题")}>
          {TERM_THEME_OPTIONS.map((option) => (
            <button
              key={option.value}
              type="button"
              className={`nx-segment-item ${prefs.terminalTheme === option.value ? "is-active" : ""}`}
              aria-pressed={prefs.terminalTheme === option.value}
              onClick={() => setTerminalTheme(option.value)}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>

      <div className="mt-3.5" data-setting="terminal-scheme">
        {SCHEME_SLOTS.map((slot) => (
          <div key={slot.kind} className="mb-3">
            <div className="mb-1.5 flex items-center gap-2">
              <span className="nx-setting-label">{slot.label}</span>
              {resolvedTermTheme === slot.kind && <span className="nx-badge">{t("使用中")}</span>}
            </div>
            <SchemeGrid
              label={slot.label}
              schemes={catalog.filter((scheme) => scheme.kind === slot.kind)}
              selected={slot.kind === "dark" ? prefs.terminalSchemeDark : prefs.terminalSchemeLight}
              onSelect={(id) => setTerminalScheme(slot.kind, id)}
            />
          </div>
        ))}
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            disabled={prefs.terminalCustomSchemes.length >= MAX_CUSTOM_SCHEMES}
            onClick={() => importInputRef.current?.click()}
          >
            <IconUpload size={11} />
            {t("导入配色")}
          </button>
          <span className="nx-hint min-w-0 text-[10.5px]">
            {t("支持 iTerm2 (.itermcolors)、Windows Terminal (JSON) 和 Ghostty/kitty 主题文件，最多 {max} 个。", { max: MAX_CUSTOM_SCHEMES })}
          </span>
          <input
            ref={importInputRef}
            type="file"
            className="hidden"
            aria-label={t("选择配色文件")}
            accept=".itermcolors,.json,.conf,.txt,.toml"
            multiple
            onChange={(e) => {
              const files = [...(e.currentTarget.files ?? [])];
              e.currentTarget.value = "";
              if (files.length > 0) void importFiles(files);
            }}
          />
        </div>
        {importStatus && (
          <div className="nx-hint mt-1.5" role="status">
            {importStatus.imported > 0 && <div>{t("已导入 {count} 个配色", { count: importStatus.imported })}</div>}
            {importStatus.problems.map((problem, i) => (
              <div key={i} className="text-[var(--nx-fg-warning)]">{problem}</div>
            ))}
          </div>
        )}
        {prefs.terminalCustomSchemes.length > 0 && (
          <ul className="mt-2 flex flex-col gap-1.5" aria-label={t("自定义配色")}>
            {prefs.terminalCustomSchemes.map((scheme) => (
              <CustomSchemeRow
                key={scheme.id}
                scheme={scheme}
                onRename={(name) =>
                  updateCustomSchemes(prefs.terminalCustomSchemes.map((item) => (item.id === scheme.id ? { ...item, name } : item)))
                }
                onDelete={() => updateCustomSchemes(prefs.terminalCustomSchemes.filter((item) => item.id !== scheme.id))}
              />
            ))}
          </ul>
        )}
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="terminal-font-size">{t("终端字号")}</div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label={t("减小终端字号")}
            disabled={prefs.terminalFontSize <= TERMINAL_FONT_SIZE_MIN}
            onClick={() => setTerminalFontSize(prefs.terminalFontSize - 1)}
          >
            <IconMinus size={11} />
          </button>
          <span className="nx-badge min-w-14 text-center">{prefs.terminalFontSize}px</span>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label={t("增大终端字号")}
            disabled={prefs.terminalFontSize >= TERMINAL_FONT_SIZE_MAX}
            onClick={() => setTerminalFontSize(prefs.terminalFontSize + 1)}
          >
            <IconPlus size={11} />
          </button>
        </div>
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="terminal-font-family">{t("终端字体")}</div>
        <Select
          className="w-full"
          ariaLabel={t("终端字体")}
          value={prefs.terminalFontFamily}
          options={TERMINAL_FONT_FAMILIES.map((family) => ({ value: family, label: TERM_FONT_LABELS[family] }))}
          onChange={(value) => setTerminalFontFamily(value as TerminalFontFamily)}
        />
        <div className="nx-hint mt-1.5">{t("只用本机已安装的字体，未安装时用系统等宽字体。")}</div>
      </div>

      <div className="mt-4">
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
          {t("高级设置")}
        </button>
        {advancedOpen && (
          <div id={advancedPanelId}>
            <div className="mt-3">
              <div className="nx-setting-label mb-1.5" data-setting="terminal-contrast">{t("终端对比度")}</div>
              <div className="nx-segment" role="group" aria-label={t("终端对比度")}>
                {TERM_CONTRAST_OPTIONS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`nx-segment-item ${prefs.terminalContrast === option.value ? "is-active" : ""}`}
                    aria-pressed={prefs.terminalContrast === option.value}
                    onClick={() => setTerminalContrast(option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
              <div className="nx-hint mt-1.5">{t("对比度只作用于 NexTerm 默认配色。")}</div>
            </div>

            <div className="mt-3.5 flex items-center gap-2">
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                title={t("恢复默认主题与外观（跟随系统、深色终端、默认配色与强调色、标准对比度、Cascadia、13px）")}
                aria-label={t("恢复默认主题与外观")}
                disabled={themeMode === "system" && appearanceIsDefault}
                onClick={resetAll}
              >
                <IconRefresh size={11} />
                {t("恢复默认")}
              </button>
              <span className="nx-hint min-w-0 text-[10.5px]">{t("只重置主题与外观，保留导入的配色。")}</span>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
